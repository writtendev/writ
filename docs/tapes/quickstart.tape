# Writ quickstart terminal cast
# Rendered by `make casts` into mp4 and webm for the docs site.
Source docs/tapes/house.tape

Output ".demo/out/quickstart.mp4"
Output ".demo/out/quickstart.webm"

Hide
Type "source docs/tapes/setup.sh"
Enter
Sleep 1s
Show

# Step 1: Initialize writ
Type "writ init"
Sleep 500ms
Enter
Sleep 2s

# Step 2: Declare the ticket type. setup.sh already wrote a writ.schema
# declaring it (editing writ.schema is not the command being demonstrated
# here); this shows applying it.
Type "writ schema apply"
Sleep 500ms
Enter
Sleep 2s

# Step 3: Create a ticket
Type 'writ object create ticket create -field "title=Add main entry point"'
Sleep 500ms
Enter
Sleep 2s

# Step 4: Apply an update
Type 'writ object apply 0192a1b2 update -field "title=Add main entry point (ready for review)"'
Sleep 500ms
Enter
Sleep 2s

# Step 5: Sync operations with remote
Type "writ sync origin"
Sleep 500ms
Enter
Sleep 2s

# Step 6: Collaborator inspects tickets in their clone
Type "cd ../collab && writ sync origin >/dev/null 2>&1"
Sleep 300ms
Enter
Sleep 1s

Type "writ object list ticket"
Sleep 500ms
Enter
Sleep 2s

Type "writ object show 0192a1b2"
Sleep 500ms
Enter
Sleep 2s

Screenshot ".demo/out/quickstart.png"
Sleep 2s
