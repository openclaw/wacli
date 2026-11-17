<!--
Optional linked context:
Add a visible `Closes #<issue-number>` or `Related: #<issue-number>` line
below this comment.

Write for someone who uses wacli but has not read the code. The title and
opening problem statement should make sense without the diff or linked issue.

Use the repository's Conventional Commit title style:
type: <what changes for the person affected>
Use a parenthesized scope only when it helps that reader.

Name the affected task and the symptom or outcome, not the implementation.
Illustrative wording, not reports of existing bugs:
Good: docs: explain how to search messages without changing the local store
Bad: docs: document the read-only flag
Good: fix: keep JSON message output readable by scripts
Bad: fix(out): move a print call to stderr

For internal-only work, name the actual developer or operator workflow affected.
-->

## What Problem This Solves

<!--
Describe the concrete user, product, or operational problem.
In 1–3 short, plain-language sentences, explain what someone is trying to do,
what goes wrong or is missing, and why it matters. For a fix, include the trigger
when known. For other changes, describe the need without inventing a bug.

Illustrative wording, not a report of an existing bug:
Good: "A script needs to search synced messages without changing the local store,
but the setup instructions do not explain how to run the search read-only."
Bad: "Documents the global read-only option."

Describe the experienced problem, not just the code change. Save function names,
missing checks, and the root-cause walkthrough for the explanation below.
-->

## User Impact

<!--
"User impact: <what users, operators, or developers can now do or expect>."
Lead with the concrete outcome in plain language, usually one sentence.
For internal-only changes, say there is no user-visible change; do not invent a benefit.
Keep important risks, breaking changes, migrations, required user actions,
and any new flags or environment variables visible here.
-->

## Why This Change Was Made

<!--
Briefly explain what changed and how it addresses the problem without repeating
the impact. Keep the body short. Leave file lists, internal acronyms, and
root-cause walkthroughs in the diff or optional <details>; include technical detail
only when it explains behavior or a material tradeoff. Do not hide risks or
required actions in <details>.
-->

## Evidence

<!--
Start with the behavior checked and the result in plain language. Reviewers
should not have to decode command lists to learn what was verified.
Summarize the relevant tests and existing repository gate results; this template
does not replace the validation requirements in AGENTS.md.

Use focused tests, CI results, redacted CLI output, or other relevant proof.
Put exact commands, commit hashes, and long output in links or optional <details>.
Keep the useful evidence summary and meaningful gaps visible. Distinguish
fake/disposable-store tests from checks with a live WhatsApp account, and do not
include private messages, contact details, session data, or credentials.
-->
