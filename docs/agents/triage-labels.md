# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual strings used in this repo's issue tracker.

This repo uses the local-markdown tracker (see `issue-tracker.md`), so these strings are values of the `Status:` line in each issue file, not GitHub labels.

| Label in mattpocock/skills | Status value in `.scratch/` issues | Meaning                                  |
| -------------------------- | ---------------------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`                     | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`                       | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`                  | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`                  | Requires human implementation            |
| `wontfix`                  | `wontfix`                          | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding status value from this table.

Edit the right-hand column to match whatever vocabulary you actually use.
