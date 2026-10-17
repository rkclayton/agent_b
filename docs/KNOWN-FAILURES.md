# Known failures

These are known test failures with scheduled follow-up work. They are not release notes or product limitations.

| Test | Where it stopped | Observed result | Blocks held | Status |
| --- | --- | --- | --- | --- |
| Packaged chat acceptance | `tests/chat-acceptance.mjs` — reopen Chat from Settings | Timed out reopening Chat from Settings. | 183 | open — item 2vm |
| Packaged chat acceptance | `tests/chat-acceptance.mjs` — worker plan result | Timed out waiting for a worker plan result. | 184, 185 | open — item 2vm |
| Packaged chat acceptance | `tests/chat-acceptance.mjs` — Settings connection Test control | Timed out finding the Settings connection Test control. | 184 | open — item 2vm |
| Packaged chat acceptance | `tests/chat-acceptance.mjs` — retained SVG journal event | Timed out waiting for the retained SVG journal event. | 185 | open — item 2vm |
| Packaged chat acceptance setup | disposable candidate signing | Timestamp service returned `0x80072ee2`; three attempts never started. | 183, 185 | open — item 2vm |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright colour measurement | Failed its colour measurement after one isolated pass. | 185 | open — item 2vn |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright menu overlay | Timed out on the menu overlay. | 185 | open — item 2vn |
