# media

Read when: downloading media from a synced message.

`wacli media` downloads media referenced by messages already stored in `wacli.db`.

## Command

```bash
wacli media download --chat JID --id MSG_ID [--output PATH]
```

## Notes

- The target message must already be synced.
- Media downloads are capped at 100 MiB.
- `--output` may be a file path or directory.
- If `--output` is omitted, media is written under the store media directory.

## Examples

```bash
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./downloads
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./photo.jpg
```
