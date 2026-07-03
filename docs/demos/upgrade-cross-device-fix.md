# upgrade: cross-device rename fix — end-to-end test

*2026-07-02T15:26:48Z by Showboat 0.6.1*
<!-- showboat-id: f7b5d534-0188-418e-8874-ba2a35435379 -->

Bug: 'token-burn upgrade' downloads to /tmp (tmpfs) and os.Rename's to the install dir (btrfs) — fails with EXDEV ('invalid cross-device link') and the deferred backup cleanup deletes the installed binary. This demo reproduces the bug with the released v0.1.14 and proves the fix on branch fix/upgrade-cross-device-rename (b84d72d).

```bash
findmnt -no TARGET,FSTYPE /tmp; findmnt -no TARGET,FSTYPE /home
```

```output
/tmp tmpfs
/home btrfs
```

## Reproduce the bug (released v0.1.14)

Install the released v0.1.14 binary at a scratch path on /home and let it upgrade itself with --force. The buggy replaceBinary destroys the binary.

```bash
TESTDIR=$HOME/.cache/token-burn-upgrade-test; rm -rf "$TESTDIR"; mkdir -p "$TESTDIR"; cp ~/.local/bin/token-burn "$TESTDIR/tb-old"; "$TESTDIR/tb-old" version | head -1; "$TESTDIR/tb-old" upgrade --binary "$TESTDIR/tb-old" --force; echo "exit: $?"; ls -la "$TESTDIR"
```

```output
token-burn v0.1.14
replace binary: rename /tmp/token-burn-upgrade-2903472622/token-burn.new /home/user/.cache/token-burn-upgrade-test/tb-old: invalid cross-device link
exit: 1
total 0
drwxr-xr-x. 1 user     user        0 Jul  2 11:27 .
drwx------. 1 user     user     1548 Jul  2 11:27 ..
```

Exact failure from the field, and the test dir is empty afterwards — the installed binary was deleted. Now build the fixed binary from fix/upgrade-cross-device-rename and run the same upgrade.

```bash
export PATH=/usr/lib/golang/bin:$PATH; cd ~/git/token-burn; git rev-parse --abbrev-ref HEAD; git rev-parse --short HEAD; go build -o ~/.cache/token-burn-upgrade-test/tb-fixed ./cmd/token-burn && echo build ok
```

```output
fix/upgrade-cross-device-rename
b84d72d
build ok
```

```bash
TESTDIR=$HOME/.cache/token-burn-upgrade-test; "$TESTDIR/tb-fixed" upgrade --binary "$TESTDIR/tb-fixed" --force; echo "exit: $?"; ls "$TESTDIR"; "$TESTDIR/tb-fixed" version | head -1
```

```output
upgraded token-burn dev -> v0.1.14 at /home/user/.cache/token-burn-upgrade-test/tb-fixed
exit: 0
tb-fixed
token-burn v0.1.14
```

Same cross-device situation (download on tmpfs /tmp, target on btrfs /home): the fixed binary falls back to copying into a staging file next to the destination and renames within one filesystem. Upgrade succeeds, binary replaced with a working v0.1.14, no .old backup left behind. The data-loss path (restore backup when install fails) is covered by unit tests in internal/upgrade/upgrade_test.go (TestReplaceBinaryRestoresBackupOnFailure).
