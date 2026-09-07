//go:generate go test ./ -run TestDocsGolden -update-docs
package main

import (
	"fmt"
	"io"
	"strings"
)

func renderDocs(w io.Writer) error {
	// Hugo front matter, so this file can be mounted directly as a site page.
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w, `title: "CLI Reference"`)
	fmt.Fprintln(w, `slug: "cli"`)
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "# CLI Reference")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "`writ` stores signed, append-only, mergeable state inside a git repository, under types your own `writ.schema` declares.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Table of Contents")
	fmt.Fprintln(w)

	var leafCommands []struct {
		path []string
		cmd  *command
	}

	for _, sub := range rootCommand.Subs {
		if len(sub.Subs) > 0 {
			for _, child := range sub.Subs {
				leafCommands = append(leafCommands, struct {
					path []string
					cmd  *command
				}{
					path: []string{sub.Name, child.Name},
					cmd:  child,
				})
			}
		} else {
			leafCommands = append(leafCommands, struct {
				path []string
				cmd  *command
			}{
				path: []string{sub.Name},
				cmd:  sub,
			})
		}
	}

	for _, item := range leafCommands {
		fullCmd := "writ " + strings.Join(item.path, " ")
		anchor := strings.ReplaceAll(fullCmd, " ", "-")
		fmt.Fprintf(w, "- [`%s`](#%s)\n", fullCmd, anchor)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Commands")
	fmt.Fprintln(w)

	for _, item := range leafCommands {
		fullCmd := "writ " + strings.Join(item.path, " ")
		fmt.Fprintf(w, "### `%s`\n\n", fullCmd)
		fmt.Fprintf(w, "%s\n\n", item.cmd.Short)

		fmt.Fprintln(w, "#### Synopsis")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "```console")
		fmt.Fprintln(w, item.cmd.UsageLine)
		fmt.Fprintln(w, "```")
		fmt.Fprintln(w)

		if item.cmd.Long != "" && item.cmd.Long != item.cmd.Short {
			fmt.Fprintln(w, "#### Description")
			fmt.Fprintln(w)
			fmt.Fprintln(w, item.cmd.Long)
			fmt.Fprintln(w)
		}

		flags := commandFlags(item.path, item.cmd)
		if len(flags) > 0 {
			fmt.Fprintln(w, "#### Flags")
			fmt.Fprintln(w)
			for _, f := range flags {
				disp := "-" + f.Name
				if f.Arg != "" {
					disp += " " + f.Arg
				}
				fmt.Fprintf(w, "- `%s`: %s\n", disp, f.Usage)
			}
			fmt.Fprintln(w)
		}

		if len(item.cmd.ExitCodes) > 0 {
			fmt.Fprintln(w, "#### Exit Codes")
			fmt.Fprintln(w)
			for _, ec := range item.cmd.ExitCodes {
				parts := strings.SplitN(strings.TrimSpace(ec), " ", 2)
				if len(parts) == 2 {
					code := parts[0]
					desc := strings.TrimSpace(parts[1])
					fmt.Fprintf(w, "- `%s`: %s\n", code, desc)
				} else {
					fmt.Fprintf(w, "- %s\n", ec)
				}
			}
			fmt.Fprintln(w)
		}

		if len(item.cmd.Examples) > 0 {
			fmt.Fprintln(w, "#### Examples")
			fmt.Fprintln(w)
			fmt.Fprintln(w, "```bash")
			for _, ex := range item.cmd.Examples {
				fmt.Fprintln(w, ex)
			}
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w)
		}
	}

	return nil
}
