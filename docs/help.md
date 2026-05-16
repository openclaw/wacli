# help

Read when: discovering command usage from the CLI itself.

`wacli help` is the Cobra-provided help command. Every command also accepts `--help`.
Root help prints the hosted documentation URL, and `wacli docs` prints it directly.

## Commands

```bash
wacli help [command]
wacli [command] --help
```

## Examples

```bash
wacli help send
wacli send text --help
wacli send status --help
wacli docs
wacli groups participants add --help
```
