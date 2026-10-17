# Issue tracker: local Markdown

Internal agent planning issues and specs live as Markdown files in
`.scratch/`. Public bug reports, feature requests and contributor questions
go to GitHub issues, as described in [Contributing](../../.github/CONTRIBUTING.md).
The conventions below apply to local planning, not to public reports.

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`
- The spec is `.scratch/<feature-slug>/spec.md`
- Implementation issues are one file per ticket at `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`. Never write a single combined tickets file.
- A ticket that belongs to no feature goes in `.scratch/worklist/issues/` and gets a line in `.scratch/worklist/README.md`. Read that README first. It names the next free number. When a ticket is done, remove its file from the folder. Worklist numbers are never reused, so the files left in the folder do not show the next free number.
- Triage state is recorded as a `Status:` line near the top of each issue file (see `triage-labels.md` for the role strings). Wayfinding tickets use other values. See [Wayfinding operations](#wayfinding-operations).
- Comments and conversation history append to the bottom of the file under a `## Comments` heading

## When a skill says "publish to the issue tracker"

Create a new file under `.scratch/<feature-slug>/` (creating the directory if needed), or under `.scratch/worklist/issues/` for a ticket that belongs to no feature.

## When a skill says "fetch the relevant ticket"

Read the file at the referenced path. The user will normally pass the path or the issue number directly. Numbers start at `01` in each feature folder, so for a number alone look in `.scratch/*/issues/NN-*.md`, and ask when more than one file matches.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a file with one **child** file per ticket. A wayfinding effort gets its own `.scratch/<feature-slug>/` folder.

- **Map.** `.scratch/<feature-slug>/map.md`, with the headings `## Destination`, `## Notes`, `## Decisions so far`, `## Not yet specified` and `## Out of scope`.
- **Child ticket.** `.scratch/<feature-slug>/issues/NN-<slug>.md`, numbered from `01`, with the question in the body. A `Type:` line records the ticket type (`research`/`prototype`/`grilling`/`task`). A `Status:` line records `claimed`/`resolved` and never a triage value.
- **Open and unclaimed.** A ticket is open until its `Status:` is `resolved`, and unclaimed while it has no `Status:` line.
- **Blocking.** A `Blocked by: NN, NN` line near the top. A ticket is unblocked when every file it lists is `resolved`.
- **Frontier.** Scan `.scratch/<feature-slug>/issues/` for files that are open, unblocked and unclaimed. The lowest number wins.
- **Claim.** Set `Status: claimed` and save before any work.
- **Resolve.** Append the answer under an `## Answer` heading, set `Status: resolved`, then append a context pointer (gist + link) to the `## Decisions so far` section of `map.md`.
