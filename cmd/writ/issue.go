package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func runIssue(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		renderUsage(stderr, []string{"issue"}, issueCmd)
		return 2
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, []string{"issue"}, issueCmd)
		return 0
	case "help":
		return runHelp(append([]string{"issue"}, args[1:]...), stdout, stderr)
	}

	targetDir := defaultDir
	if args[0] == "-C" {
		if len(args) < 2 {
			fmt.Fprintln(stderr, "writ issue: option -C requires an argument")
			return 2
		}
		targetDir = args[1]
		args = args[2:]
		if len(args) == 0 {
			renderUsage(stderr, []string{"issue"}, issueCmd)
			return 2
		}
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, []string{"issue"}, issueCmd)
		return 0
	case "help":
		return runHelp(append([]string{"issue"}, args[1:]...), stdout, stderr)
	case "create":
		return runIssueCreate(ctx, targetDir, args[1:], stdout, stderr)
	case "update":
		return runIssueUpdate(ctx, targetDir, args[1:], stdout, stderr)
	case "status":
		return runIssueStatus(ctx, targetDir, args[1:], stdout, stderr)
	case "comment":
		return runIssueComment(ctx, targetDir, args[1:], stdout, stderr)
	case "assign":
		return runIssueAssign(ctx, targetDir, args[1:], stdout, stderr)
	case "list":
		return runIssueList(ctx, targetDir, args[1:], stdout, stderr)
	case "link":
		return runIssueLink(ctx, targetDir, args[1:], stdout, stderr)
	case "label":
		return runIssueLabel(ctx, targetDir, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "writ issue: unknown subcommand %q\n\n", args[0])
		renderUsage(stderr, []string{"issue"}, issueCmd)
		return 2
	}
}

type issueCreateOpts struct {
	dir         string
	title       string
	description string
	stateVal    string
	priority    string
	estimate    string
	position    string
	fixes       stringSliceFlag
	relates     stringSliceFlag
}

func newIssueCreateFlagSet(defaultDir string) (*flag.FlagSet, *issueCreateOpts) {
	fs := flag.NewFlagSet("issue create", flag.ContinueOnError)
	opts := &issueCreateOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.title, "title", "", "Issue title `<t>` (required)")
	fs.StringVar(&opts.description, "description", "", "Issue description `<d>`")
	fs.StringVar(&opts.stateVal, "state", "", "Initial issue state `<state>` (workflow-state name or ID)")
	fs.StringVar(&opts.priority, "priority", "", "Issue priority `<p>` (urgent|high|medium|low|none or 0..4)")
	fs.StringVar(&opts.estimate, "estimate", "", "Issue estimate `<e>` (non-negative number)")
	fs.StringVar(&opts.position, "position", "", "Issue position `<pos>` (fractional index)")
	fs.Var(&opts.fixes, "fixes", "Add a 'fixes' cross-reference link `<ref>` (repeatable)")
	fs.Var(&opts.relates, "relates", "Add a 'relates' cross-reference link `<ref>` (repeatable)")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "create"}, issueCreateCmd)
	}
	return fs, opts
}

func runIssueCreate(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueCreateFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) > 0 {
		fmt.Fprintf(stderr, "writ issue create: unexpected arguments: %s\n", strings.Join(posArgs, " "))
		fs.Usage()
		return 2
	}

	if opts.title == "" {
		fmt.Fprintln(stderr, "writ issue create: -title is required")
		fs.Usage()
		return 2
	}

	var priorityPtr *int
	if opts.priority != "" {
		p, err := parsePriority(opts.priority)
		if err != nil {
			fmt.Fprintf(stderr, "writ issue create: %v\n", err)
			fs.Usage()
			return 2
		}
		priorityPtr = &p
	}
	var estimatePtr *float64
	if opts.estimate != "" {
		est, err := parseEstimate(opts.estimate)
		if err != nil {
			fmt.Fprintf(stderr, "writ issue create: %v\n", err)
			fs.Usage()
			return 2
		}
		estimatePtr = est
	}
	var positionPtr *string
	if opts.position != "" {
		positionPtr = &opts.position
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	var targetStateID string
	if opts.stateVal != "" {
		resolvedID, err := resolveStateID(ctx, store, opts.stateVal)
		if err == nil {
			targetStateID = resolvedID
		} else {
			fmt.Fprintf(stderr, "writ issue create: invalid state %q\n", opts.stateVal)
			fs.Usage()
			return 2
		}
	} else {
		states, err := store.Query.WorkflowStates(writ.WorkflowStateFilter{})
		if err == nil && len(states) > 0 {
			for _, s := range states {
				if s.WorkflowState.Type == "unstarted" {
					targetStateID = s.ObjectID
					break
				}
			}
			if targetStateID == "" {
				for _, s := range states {
					if s.WorkflowState.Type == "backlog" {
						targetStateID = s.ObjectID
						break
					}
				}
			}
			if targetStateID == "" {
				targetStateID = states[0].ObjectID
			}
		}
	}

	id, err := store.Issues.Create(ctx, writ.NewIssue{
		Title:       opts.title,
		Description: opts.description,
		Priority:    priorityPtr,
		Estimate:    estimatePtr,
		Position:    positionPtr,
	})
	if err != nil {
		return renderErr(stderr, err)
	}

	if targetStateID != "" {
		if err := store.Issues.SetState(ctx, id, writ.IssueState{State: targetStateID}); err != nil {
			return renderErr(stderr, err)
		}
	}

	for _, fix := range opts.fixes {
		if err := store.Issues.Link(ctx, id, writ.Link{Target: fix, Relation: "fixes"}); err != nil {
			return renderErr(stderr, err)
		}
	}

	for _, rel := range opts.relates {
		if err := store.Issues.Link(ctx, id, writ.Link{Target: rel, Relation: "relates"}); err != nil {
			return renderErr(stderr, err)
		}
	}

	dispState := opts.stateVal
	if dispState == "" {
		dispState = "-"
	}
	if targetStateID != "" {
		ws, err := store.Query.WorkflowState(targetStateID)
		if err == nil && ws.WorkflowState.Name != "" {
			dispState = ws.WorkflowState.Name
		}
	}

	fmt.Fprintf(stdout, "%s (%s) %s\n", id, dispState, opts.title)
	return 0
}

type issueUpdateOpts struct {
	dir         string
	title       string
	description string
	priority    string
	estimate    string
	position    string
}

func newIssueUpdateFlagSet(defaultDir string) (*flag.FlagSet, *issueUpdateOpts) {
	fs := flag.NewFlagSet("issue update", flag.ContinueOnError)
	opts := &issueUpdateOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.title, "title", "", "Updated title `<t>`")
	fs.StringVar(&opts.description, "description", "", "Updated description `<d>`")
	fs.StringVar(&opts.priority, "priority", "", "Updated priority `<p>` (urgent|high|medium|low|none or 0..4)")
	fs.StringVar(&opts.estimate, "estimate", "", "Updated estimate `<e>` (non-negative number)")
	fs.StringVar(&opts.position, "position", "", "Updated position `<pos>` (fractional index)")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "update"}, issueUpdateCmd)
	}
	return fs, opts
}

func runIssueUpdate(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueUpdateFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue update: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ issue update: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	var edit writ.IssueEdit
	var hasChange bool
	var priorityVal string
	var estimateVal string
	var prioritySet, estimateSet bool

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "title":
			edit.Title = &opts.title
			hasChange = true
		case "description":
			edit.Description = &opts.description
			hasChange = true
		case "position":
			edit.Position = &opts.position
			hasChange = true
		case "priority":
			prioritySet = true
			priorityVal = f.Value.String()
			hasChange = true
		case "estimate":
			estimateSet = true
			estimateVal = f.Value.String()
			hasChange = true
		}
	})

	if prioritySet {
		p, err := parsePriority(priorityVal)
		if err != nil {
			fmt.Fprintf(stderr, "writ issue update: %v\n", err)
			fs.Usage()
			return 2
		}
		edit.Priority = &p
	}

	if estimateSet {
		est, err := parseEstimate(estimateVal)
		if err != nil {
			fmt.Fprintf(stderr, "writ issue update: %v\n", err)
			fs.Usage()
			return 2
		}
		edit.Estimate = est
	}

	if !hasChange {
		fmt.Fprintln(stderr, "writ issue update: at least one field to update must be specified")
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	if err := store.Issues.Update(ctx, issueID, edit); err != nil {
		return renderErr(stderr, err)
	}

	fmt.Fprintf(stdout, "%s updated\n", issueID)
	return 0
}

func parsePriority(val string) (int, error) {
	if val == "" {
		return 0, nil
	}
	p, err := spec.ParseIssuePriority(val)
	if err == nil {
		return p, nil
	}
	if n, err := strconv.Atoi(val); err == nil && n >= 0 && n <= 4 {
		return n, nil
	}
	return 0, fmt.Errorf("invalid priority %q: must be one of urgent, high, medium, low, none, or 0..4", val)
}

func parseEstimate(val string) (*float64, error) {
	if val == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("invalid estimate %q: must be a non-negative number", val)
	}
	return &f, nil
}

type issueStatusOpts struct {
	dir      string
	reason   string
	position string
	jsonMode bool
}

func newIssueStatusFlagSet(defaultDir string) (*flag.FlagSet, *issueStatusOpts) {
	fs := flag.NewFlagSet("issue status", flag.ContinueOnError)
	opts := &issueStatusOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.reason, "reason", "", "Reason `<r>` for status change")
	fs.StringVar(&opts.position, "position", "", "Updated position `<pos>` (fractional index)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON (view mode only)")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "status"}, issueStatusCmd)
	}
	return fs, opts
}

func runIssueStatus(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueStatusFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue status: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 2 {
		fmt.Fprintf(stderr, "writ issue status: unexpected arguments: %s\n", strings.Join(posArgs[2:], " "))
		fs.Usage()
		return 2
	}

	if len(posArgs) == 1 {
		// No explicit new state: view mode (no -position) and
		// reposition-only mode (-position set) both leave -reason with
		// nothing to attach to. Reject it here, before the reposition-only
		// path is chosen below, so the flag's validity does not depend on
		// whether this issue happens to already have a folded state.
		if opts.reason != "" {
			fmt.Fprintln(stderr, "writ issue status: -reason is only valid when setting status")
			fs.Usage()
			return 2
		}
	}

	if len(posArgs) == 2 {
		if opts.jsonMode {
			fmt.Fprintln(stderr, "writ issue status: --json is only valid when viewing status")
			fs.Usage()
			return 2
		}
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	var newState string
	if len(posArgs) == 2 {
		rawState := posArgs[1]
		resolvedID, err := resolveStateID(ctx, store, rawState)
		if err == nil {
			newState = resolvedID
		} else {
			fmt.Fprintf(stderr, "writ issue status: invalid status %q\n", rawState)
			fs.Usage()
			return 2
		}
	}

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	// 1. Read / View status mode (len(posArgs) == 1 and no -position)
	if len(posArgs) == 1 && opts.position == "" {

		res, err := store.Query.Issue(issueID)
		if err != nil {
			return renderErr(stderr, err)
		}

		threads, err := store.Query.Threads("issue", issueID)
		if err != nil {
			return renderErr(stderr, err)
		}

		if opts.jsonMode {
			var labelNames []string
			for _, l := range res.Issue.Labels {
				displayName := l
				if lbl, err := store.Query.Label(l); err == nil {
					displayName = lbl.Label.Name
				}
				labelNames = append(labelNames, displayName)
			}
			sort.Strings(labelNames)
			res.Issue.Labels = labelNames

			wireIssue := wire.FromIssueResult(res, threads)
			if err := emitJSON(stdout, wire.KindIssueStatus, wireIssue); err != nil {
				fmt.Fprintf(stderr, "writ issue status: marshal json: %v\n", err)
				return 1
			}
			return 0
		}

		stateVal := res.Issue.State
		if stateVal == "" {
			stateVal = "-"
		} else {
			ws, err := store.Query.WorkflowState(stateVal)
			if err == nil && ws.WorkflowState.Name != "" {
				stateVal = ws.WorkflowState.Name
			}
		}
		author := authorDisplay(res.Author.Name, res.Author.Email)

		var assignees string
		if len(res.Issue.Assignees) > 0 {
			assignees = strings.Join(res.Issue.Assignees, ", ")
		} else {
			assignees = "-"
		}

		var labelNames []string
		for _, l := range res.Issue.Labels {
			displayName := l
			if lbl, err := store.Query.Label(l); err == nil {
				displayName = lbl.Label.Name
			}
			labelNames = append(labelNames, displayName)
		}
		sort.Strings(labelNames)
		var labels string
		if len(labelNames) > 0 {
			labels = strings.Join(labelNames, ", ")
		} else {
			labels = "-"
		}

		fmt.Fprintf(stdout, "Issue:       %s\n", res.ObjectID)
		fmt.Fprintf(stdout, "Title:       %s\n", res.Issue.Title)
		fmt.Fprintf(stdout, "State:       %s\n", stateVal)
		if res.Issue.Reason != "" {
			fmt.Fprintf(stdout, "Reason:      %s\n", res.Issue.Reason)
		}
		if res.Issue.Priority > 0 {
			fmt.Fprintf(stdout, "Priority:    %s\n", spec.FormatIssuePriority(res.Issue.Priority))
		}
		if res.Issue.Estimate != nil {
			fmt.Fprintf(stdout, "Estimate:    %g\n", *res.Issue.Estimate)
		}
		if res.Issue.Position != "" {
			fmt.Fprintf(stdout, "Position:    %s\n", res.Issue.Position)
		}
		fmt.Fprintf(stdout, "Author:      %s\n", author)
		fmt.Fprintf(stdout, "Assignees:   %s\n", assignees)
		fmt.Fprintf(stdout, "Labels:      %s\n", labels)

		if len(res.Issue.Links) > 0 {
			fmt.Fprintln(stdout, "Links:")
			for _, link := range res.Issue.Links {
				scope, _, err := resolveIssueRef(ctx, store, link.Target)
				var outcome string
				if err != nil || scope == "unresolved" {
					outcome = "unresolved"
				} else {
					outcome = "local"
				}
				fmt.Fprintf(stdout, "  %s %s (%s)\n", link.Relation, link.Target, outcome)
			}
		}

		if len(threads) > 0 {
			fmt.Fprintln(stdout, "Comments:")
			renderCommentThreads(stdout, threads, 1)
		}
		return 0
	}

	// 2. Update / Transition status mode
	if opts.jsonMode {
		fmt.Fprintln(stderr, "writ issue status: --json is only valid when viewing status")
		fs.Usage()
		return 2
	}

	if len(posArgs) == 1 {
		currentIssue, err := store.Query.Issue(issueID)
		if err != nil {
			return renderErr(stderr, err)
		}
		newState = currentIssue.Issue.State
	}

	var posPtr *string
	if opts.position != "" {
		posPtr = &opts.position
	}

	if newState == "" {
		// Reposition-only path (len(posArgs) == 1) on an issue that has
		// never had a set-state op: there is no state to pass through,
		// and SetState rejects "". Move the position via the update op
		// instead of forcing a state onto an issue that has none.
		if err := store.Issues.Update(ctx, issueID, writ.IssueEdit{Position: posPtr}); err != nil {
			return renderErr(stderr, err)
		}
	} else {
		if err := store.Issues.SetState(ctx, issueID, writ.IssueState{
			State:    newState,
			Reason:   opts.reason,
			Position: posPtr,
		}); err != nil {
			return renderErr(stderr, err)
		}
	}

	dispState := newState
	if dispState == "" {
		dispState = "-"
	} else if ws, err := store.Query.WorkflowState(newState); err == nil && ws.WorkflowState.Name != "" {
		dispState = ws.WorkflowState.Name
	}

	fmt.Fprintf(stdout, "%s: %s\n", issueID, dispState)
	return 0
}

func renderCommentThreads(w io.Writer, threads []state.CommentThread, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, t := range threads {
		var annotation string
		if t.Comment.Deleted {
			annotation = " (deleted)"
		} else if t.Comment.IsResolved() {
			if t.Comment.ResolvedBy != "" {
				annotation = fmt.Sprintf(" (resolved by %s)", t.Comment.ResolvedBy)
			} else {
				annotation = " (resolved)"
			}
		}

		if t.Comment.Text != "" {
			fmt.Fprintf(w, "%s[%s] %s%s\n", indent, t.ObjectID, t.Comment.Text, annotation)
		} else {
			fmt.Fprintf(w, "%s[%s]%s\n", indent, t.ObjectID, annotation)
		}
		if len(t.Replies) > 0 {
			renderCommentThreads(w, t.Replies, depth+1)
		}
	}
}

type issueCommentOpts struct {
	dir       string
	message   string
	replyTo   string
	resolve   bool
	unresolve bool
}

func newIssueCommentFlagSet(defaultDir string) (*flag.FlagSet, *issueCommentOpts) {
	fs := flag.NewFlagSet("issue comment", flag.ContinueOnError)
	opts := &issueCommentOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.message, "m", "", "Comment text `<text>`")
	fs.StringVar(&opts.replyTo, "reply-to", "", "Comment ID `<comment-id>` to reply to")
	fs.BoolVar(&opts.resolve, "resolve", false, "Mark comment thread as resolved, attributed to writ.personId, else email:<user.email>")
	fs.BoolVar(&opts.unresolve, "unresolve", false, "Mark comment thread as unresolved, preserving the recorded resolver")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "comment"}, issueCommentCmd)
	}
	return fs, opts
}

func runIssueComment(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueCommentFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue comment: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ issue comment: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	if opts.resolve && opts.unresolve {
		fmt.Fprintln(stderr, "writ issue comment: cannot specify both -resolve and -unresolve")
		fs.Usage()
		return 2
	}

	if opts.message == "" && !opts.resolve && !opts.unresolve {
		fmt.Fprintln(stderr, "writ issue comment: -m is required (or specify -resolve / -unresolve)")
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	var directCommentID string
	if err != nil {
		// Check if posArgs[0] is a comment ID prefix
		comments, cErr := store.Query.Comments(writ.CommentFilter{
			SubjectType:    "issue",
			IncludeDeleted: true,
		})
		if cErr == nil {
			var matches []writ.CommentResult
			for _, c := range comments {
				if strings.HasPrefix(c.ObjectID, posArgs[0]) && c.Comment.Subject.ObjectType == "issue" {
					matches = append(matches, c)
				}
			}
			if len(matches) == 1 && matches[0].Comment.Subject.ObjectID != "" {
				issueID = matches[0].Comment.Subject.ObjectID
				directCommentID = matches[0].ObjectID
				err = nil
			} else if len(matches) > 1 {
				return renderErr(stderr, fmt.Errorf("ambiguous ID prefix %q matches multiple comments", posArgs[0]))
			}
		}
	}
	if err != nil {
		return renderErr(stderr, err)
	}

	targetCommentLookup := opts.replyTo
	if targetCommentLookup == "" && directCommentID != "" {
		targetCommentLookup = directCommentID
	}

	var replyToID string
	var threadRootID string

	if targetCommentLookup != "" {
		comments, err := store.Query.Comments(writ.CommentFilter{
			SubjectType:    "issue",
			SubjectID:      issueID,
			IncludeDeleted: true,
		})
		if err != nil {
			return renderErr(stderr, err)
		}
		var matches []writ.CommentResult
		for _, c := range comments {
			if strings.HasPrefix(c.ObjectID, targetCommentLookup) {
				matches = append(matches, c)
			}
		}
		var matchedComment writ.CommentResult
		if len(matches) == 1 {
			matchedComment = matches[0]
			replyToID = matches[0].ObjectID
		} else if len(matches) > 1 {
			var matchIDs []string
			for _, m := range matches {
				matchIDs = append(matchIDs, m.ObjectID)
			}
			return renderErr(stderr, fmt.Errorf("ambiguous comment ID prefix %q matches %d comments (%s)", targetCommentLookup, len(matches), strings.Join(matchIDs, ", ")))
		} else {
			return renderErr(stderr, fmt.Errorf("comment %q not found on issue", targetCommentLookup))
		}

		parentMap := make(map[string]string, len(comments))
		for _, c := range comments {
			if c.Comment.InReplyTo != "" {
				parentMap[c.ObjectID] = c.Comment.InReplyTo
			}
		}
		curr := matchedComment.ObjectID
		visited := make(map[string]bool, len(comments))
		for parentMap[curr] != "" && !visited[curr] {
			visited[curr] = true
			curr = parentMap[curr]
		}
		threadRootID = curr
	}

	var resolvedBy string
	if opts.resolve {
		writer := store.Writer()
		resolvedBy = writer.PersonID
		if resolvedBy == "" {
			if writer.PersonIDErr != nil {
				return renderErr(stderr, fmt.Errorf("writ: no resolver identity: %w", writer.PersonIDErr))
			}
			return renderErr(stderr, fmt.Errorf("writ: no resolver identity: configure %s (for example %q) or user.email", identity.PersonIDKey, "user:alice"))
		}
	}

	var commentID string
	if opts.message != "" {
		cid, err := store.Issues.Comment(ctx, issueID, writ.NewComment{
			Text:      opts.message,
			InReplyTo: replyToID,
		})
		if err != nil {
			return renderErr(stderr, err)
		}
		commentID = cid
	}

	if opts.resolve || opts.unresolve {
		resolveTarget := threadRootID
		if resolveTarget == "" {
			if commentID != "" {
				resolveTarget = commentID
			} else {
				return renderErr(stderr, fmt.Errorf("writ issue comment: comment or thread ID is required to resolve"))
			}
		}

		if err := store.Comments.Resolve(ctx, resolveTarget, writ.CommentResolve{
			Resolved:   opts.resolve,
			ResolvedBy: resolvedBy,
		}); err != nil {
			return renderErr(stderr, err)
		}

		if commentID == "" {
			action := "resolved"
			if opts.unresolve {
				action = "unresolved"
			}
			fmt.Fprintf(stdout, "%s (%s)\n", resolveTarget, action)
			return 0
		}
	}

	fmt.Fprintln(stdout, commentID)
	return 0
}

type issueAssignOpts struct {
	dir    string
	add    stringSliceFlag
	remove stringSliceFlag
}

func newIssueAssignFlagSet(defaultDir string) (*flag.FlagSet, *issueAssignOpts) {
	fs := flag.NewFlagSet("issue assign", flag.ContinueOnError)
	opts := &issueAssignOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.add, "add", "Add assignee `<a>`, a scheme:value person identifier (repeatable)")
	fs.Var(&opts.remove, "remove", "Remove assignee `<a>`, a scheme:value person identifier (repeatable)")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "assign"}, issueAssignCmd)
	}
	return fs, opts
}

func runIssueAssign(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueAssignFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue assign: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ issue assign: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	if len(opts.add) == 0 && len(opts.remove) == 0 {
		fmt.Fprintln(stderr, "writ issue assign: at least one -add or -remove is required")
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	if err := store.Issues.Assign(ctx, issueID, opts.add, opts.remove); err != nil {
		return renderErr(stderr, err)
	}

	fmt.Fprintf(stdout, "%s: updated assignees\n", issueID)
	return 0
}

type issueListOpts struct {
	dir        string
	states     stringSliceFlag
	assignees  stringSliceFlag
	labels     stringSliceFlag
	authors    stringSliceFlag
	priorities stringSliceFlag
	text       string
	limit      int
	sortOrder  string
	jsonMode   bool
}

func newIssueListFlagSet(defaultDir string) (*flag.FlagSet, *issueListOpts) {
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	opts := &issueListOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.states, "state", "Filter by issue state `<s>` (repeatable)")
	fs.Var(&opts.assignees, "assignee", "Filter by assignee `<a>`, a scheme:value person identifier (repeatable)")
	fs.Var(&opts.labels, "label", "Filter by label `<l>` (repeatable)")
	fs.Var(&opts.authors, "author", "Filter by author `<a>` name or email (repeatable)")
	fs.Var(&opts.priorities, "priority", "Filter by priority `<p>` (urgent|high|medium|low|none or 0..4) (repeatable)")
	fs.StringVar(&opts.text, "text", "", "Filter by text `<q>` match in title or description")
	fs.IntVar(&opts.limit, "limit", 0, "Maximum number `N` of issues to return")
	fs.StringVar(&opts.sortOrder, "sort", "", "Sort order `<order>` (created_at_asc, created_at_desc, updated_at_asc, updated_at_desc, title_asc, title_desc, priority_asc, priority_desc, position_asc, position_desc, estimate_asc, estimate_desc)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "list"}, issueListCmd)
	}
	return fs, opts
}

func runIssueList(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueListFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) > 0 {
		fmt.Fprintf(stderr, "writ issue list: unexpected arguments: %s\n", strings.Join(posArgs, " "))
		fs.Usage()
		return 2
	}

	if opts.limit < 0 {
		fmt.Fprintf(stderr, "writ issue list: -limit must be non-negative, got %d\n", opts.limit)
		fs.Usage()
		return 2
	}

	var priorityFilter []int
	if len(opts.priorities) > 0 {
		for _, pStr := range opts.priorities {
			p, err := parsePriority(pStr)
			if err != nil {
				fmt.Fprintf(stderr, "writ issue list: %v\n", err)
				fs.Usage()
				return 2
			}
			priorityFilter = append(priorityFilter, p)
		}
	}

	var orderBy writ.OrderBy
	if opts.sortOrder != "" {
		var err error
		orderBy, err = parseOrderBy(opts.sortOrder)
		if err != nil {
			fmt.Fprintf(stderr, "writ issue list: invalid sort order %q\n", opts.sortOrder)
			fs.Usage()
			return 2
		}
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issues, err := store.Query.Issues(writ.IssueFilter{
		State:    opts.states,
		Assignee: opts.assignees,
		Label:    opts.labels,
		Author:   opts.authors,
		Priority: priorityFilter,
		Text:     opts.text,
		Limit:    opts.limit,
		OrderBy:  orderBy,
	})
	if err != nil {
		return renderErr(stderr, err)
	}

	if opts.jsonMode {
		wireSummaries := wire.FromIssueResultSummaries(issues)
		if err := emitJSON(stdout, wire.KindIssueList, wireSummaries); err != nil {
			fmt.Fprintf(stderr, "writ issue list: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	stateNames := make(map[string]string)
	if wStates, err := store.Query.WorkflowStates(writ.WorkflowStateFilter{}); err == nil {
		for _, ws := range wStates {
			stateNames[ws.ObjectID] = ws.WorkflowState.Name
		}
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, iss := range issues {
		shortID := iss.ObjectID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		stateVal := iss.Issue.State
		if stateVal == "" {
			stateVal = "-"
		} else if name, ok := stateNames[stateVal]; ok && name != "" {
			stateVal = name
		}
		pStr := "-"
		if iss.Issue.Priority > 0 {
			pStr = spec.FormatIssuePriority(iss.Issue.Priority)
		}
		estStr := "-"
		if iss.Issue.Estimate != nil {
			estStr = fmt.Sprintf("%g", *iss.Issue.Estimate)
		}
		assigneesStr := "-"
		if len(iss.Issue.Assignees) > 0 {
			assigneesStr = strings.Join(iss.Issue.Assignees, ", ")
		}
		updatedAt := iss.UpdatedAt.Format("2006-01-02 15:04:05")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", shortID, stateVal, pStr, estStr, iss.Issue.Title, assigneesStr, updatedAt)
	}
	_ = tw.Flush()
	return 0
}

type issueLinkOpts struct {
	dir        string
	target     string
	relation   string
	targetType string
}

func newIssueLinkFlagSet(defaultDir string) (*flag.FlagSet, *issueLinkOpts) {
	fs := flag.NewFlagSet("issue link", flag.ContinueOnError)
	opts := &issueLinkOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.target, "target", "", "Target reference `<ref>` (required, e.g. <repo-id>#<object-id> or <object-id>)")
	fs.StringVar(&opts.relation, "relation", "", "Link relation `<rel>`: fixes, relates, or none (required)")
	fs.StringVar(&opts.targetType, "target-type", "", "Target object type `<t>`")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "link"}, issueLinkCmd)
	}
	return fs, opts
}

func runIssueLink(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueLinkFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue link: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ issue link: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	if opts.target == "" {
		fmt.Fprintln(stderr, "writ issue link: -target is required")
		fs.Usage()
		return 2
	}

	if opts.relation == "" {
		fmt.Fprintln(stderr, "writ issue link: -relation is required")
		fs.Usage()
		return 2
	}

	if !slices.Contains(spec.LinkRelations(), opts.relation) {
		fmt.Fprintf(stderr, "writ issue link: invalid relation %q (must be %s)\n", opts.relation, spec.FormatOptions(spec.LinkRelations()))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	if err := store.Issues.Link(ctx, issueID, writ.Link{
		Target:     opts.target,
		Relation:   opts.relation,
		TargetType: opts.targetType,
	}); err != nil {
		return renderErr(stderr, err)
	}

	fmt.Fprintf(stdout, "%s: link %s -> %s\n", issueID, opts.relation, opts.target)
	return 0
}

type issueLabelOpts struct {
	dir      string
	add      stringSliceFlag
	remove   stringSliceFlag
	jsonMode bool
}

func newIssueLabelFlagSet(defaultDir string) (*flag.FlagSet, *issueLabelOpts) {
	fs := flag.NewFlagSet("issue label", flag.ContinueOnError)
	opts := &issueLabelOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.Var(&opts.add, "add", "Add label `<l>` (repeatable)")
	fs.Var(&opts.remove, "remove", "Remove label `<l>` (repeatable)")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"issue", "label"}, issueLabelCmd)
	}
	return fs, opts
}

func runIssueLabel(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newIssueLabelFlagSet(defaultDir)
	fs.SetOutput(stderr)

	posArgs, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if len(posArgs) == 0 || posArgs[0] == "" {
		fmt.Fprintln(stderr, "writ issue label: issue ID is required")
		fs.Usage()
		return 2
	}
	if len(posArgs) > 1 {
		fmt.Fprintf(stderr, "writ issue label: unexpected arguments: %s\n", strings.Join(posArgs[1:], " "))
		fs.Usage()
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := openStore(targetDir)
	if err != nil {
		return renderErr(stderr, err)
	}
	defer store.Close()

	issueID, err := resolveIssueID(ctx, store, posArgs[0])
	if err != nil {
		return renderErr(stderr, err)
	}

	res, err := store.Query.Issue(issueID)
	if err != nil {
		return renderErr(stderr, err)
	}

	if len(opts.add) == 0 && len(opts.remove) == 0 {
		var labelNames []string
		for _, l := range res.Issue.Labels {
			displayName := l
			if lbl, err := store.Query.Label(l); err == nil {
				displayName = lbl.Label.Name
			}
			labelNames = append(labelNames, displayName)
		}
		sort.Strings(labelNames)
		if opts.jsonMode {
			if err := emitJSON(stdout, wire.KindIssueLabel, wire.FromIssueLabels(issueID, labelNames)); err != nil {
				fmt.Fprintf(stderr, "writ issue label: marshal json: %v\n", err)
				return 1
			}
			return 0
		}
		for _, l := range labelNames {
			fmt.Fprintln(stdout, l)
		}
		return 0
	}

	resolvedAdd, resolvedRemove, err := resolveLabelsForModification(ctx, store, res.Issue.Labels, opts.add, opts.remove)
	if err != nil {
		return renderErr(stderr, err)
	}

	if err := store.Issues.Label(ctx, issueID, resolvedAdd, resolvedRemove); err != nil {
		return renderErr(stderr, err)
	}

	if opts.jsonMode {
		updatedRes, err := store.Query.Issue(issueID)
		if err != nil {
			return renderErr(stderr, err)
		}
		var labelNames []string
		for _, l := range updatedRes.Issue.Labels {
			displayName := l
			if lbl, err := store.Query.Label(l); err == nil {
				displayName = lbl.Label.Name
			}
			labelNames = append(labelNames, displayName)
		}
		sort.Strings(labelNames)
		if err := emitJSON(stdout, wire.KindIssueLabel, wire.FromIssueLabels(issueID, labelNames)); err != nil {
			fmt.Fprintf(stderr, "writ issue label: marshal json: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "%s: updated labels\n", issueID)
	return 0
}
