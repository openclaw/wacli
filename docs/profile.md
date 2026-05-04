# profile

Read when: setting the profile picture for the authenticated WhatsApp account.

`wacli profile` manages account-level WhatsApp profile settings for the linked account.

## Command

```bash
wacli profile set-picture <image>
```

## Notes

- `set-picture` requires authentication, a live connection, and writable mode.
- Input can be JPEG or PNG.
- PNG transparency is flattened onto a white background before upload.
- Images larger than 640 px on either side are resized before upload.
- The command prints the picture ID returned by WhatsApp; use `--json` for machine-readable output.

## Examples

```bash
wacli profile set-picture ./avatar.jpg
wacli profile set-picture ./avatar.png --json
```
