# Share space

This terminal runs in a browser at term.ab18.in, where long output is hard to read and copy. Each session also has a **share space**: a directory whose files appear on a web viewer next to this session's chat history. A SessionStart hook gives you the session's directory and viewer URL at the start of the session. Look for "Share space for this session" in your context.

## When to publish

Write a file to the share directory instead of printing in the terminal when the output is:
- a design, plan, proposal, report, review, write-up, summary or explanation longer than ~40 lines
- a table longer than ~10 rows, or any comparison matrix
- a diagram, chart or anything visual
- something the user asks to share, show, view, write up or open in the browser

Short answers, status updates and conversation stay in the terminal as usual.

## How to publish

- **Markdown (`.md`)** is the default. Start the file with a `# Title` line, which the viewer uses as the document's title. Fenced code blocks get syntax highlighting.
- **HTML (`.html`)** is for visual or interactive content. Make it a single self-contained file with inline CSS/JS, and give it a `<title>`. It runs in a sandboxed iframe without cookies or same-origin access.
- **Code, data or logs:** save the file with its real extension (`.py`, `.json`, `.csv`, `.log` …).
- **Images:** `.svg`, `.png`, `.jpg`, `.webp`. Markdown can embed images from the same directory with a relative path (`![chart](chart.svg)`).
- **File names:** lowercase-kebab-case, flat (no subdirectories), e.g. `share-viewer-design.md`. To revise a document, overwrite the same file.
- **Never** write secrets (tokens, passwords, private keys, `.env` values) into a shared file.

## After publishing

Reply in the terminal with:
- the direct link, `<viewer URL>#<file-name>`
- a 2–5 line summary of what the document says

Don't also paste the whole document into the terminal.
