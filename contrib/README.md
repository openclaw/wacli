# contrib — Service & Deployment Examples

This directory contains example service configurations for running `wacli sync --follow` as a background daemon.

## systemd (Linux)

### Single account

Copy `systemd/wacli-sync.service` to `~/.config/systemd/user/` (user mode) or `/etc/systemd/system/` (system mode), then:

```bash
# User mode
systemctl --user daemon-reload
systemctl --user enable --now wacli-sync

# Check logs
journalctl --user -u wacli-sync -f
```

### Multiple accounts (template unit)

Use the template `systemd/wacli-sync@.service` with instance names:

```bash
cp systemd/wacli-sync@.service ~/.config/systemd/user/
systemctl --user daemon-reload

# Start sync for a specific store directory name under ~/.wacli/
systemctl --user enable --now wacli-sync@personal
systemctl --user enable --now wacli-sync@work
```

Each instance uses `~/.wacli/%i` as its store directory.

## launchd (macOS)

Copy `launchd/com.wacli.sync.plist` to `~/Library/LaunchAgents/`, then:

```bash
launchctl load ~/Library/LaunchAgents/com.wacli.sync.plist
launchctl start com.wacli.sync

# Check logs
tail -f /tmp/wacli-sync.log
```

## Homebrew (macOS/Linux)

See `homebrew/wacli.rb` for a basic formula example. This is a starting point — adjust the version, SHA, and URL for actual releases.

```bash
brew install --build-from-source homebrew/wacli.rb
```

## Tips

- **Authentication first:** Run `wacli auth` interactively before starting the daemon. The service expects an already-authenticated session.
- **Socket IPC:** When `wacli sync --follow` is running, it creates a Unix socket (`~/.wacli/wacli.sock`) that allows `wacli send` and `wacli read` to work without acquiring the lock.
- **Output modes:** Use `--output json` or `--output text` to stream incoming messages to stdout (useful for piping to other tools).
- **Read receipts:** Add `--mark-read` to automatically mark incoming messages as read.
