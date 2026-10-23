# Known failures

These are known test failures with scheduled follow-up work. They are not release notes or product limitations.

| Test | Where it stopped | Observed result | Blocks held | Status |
| --- | --- | --- | --- | --- |
| Packaged chat acceptance | `settings-pages` journey — reopen Chat from Settings | Timed out reopening Chat from Settings. | 183 | moved — item 2vm |
| Packaged chat acceptance | `worker-plans` journey — worker plan result | Timed out waiting for a worker plan result. | 184, 185 | moved — item 2vm |
| Packaged chat acceptance | `connections` journey — Settings connection Test control | Timed out finding the Settings connection Test control. | 184 | moved — item 2vm |
| Packaged chat acceptance | `attachments-and-ocr` journey — retained SVG journal event | Timed out waiting for the retained SVG journal event. | 185 | moved — item 2vm |
| Packaged chat acceptance setup | disposable candidate signing | Untimestamped test signing completed with timestamp networking blocked. | 183, 185 | closed — item 2vm |
| `scheduled-work` journey | silent scheduled job chat cleanup | The silent job left a Scheduled chat on both isolated attempts. | 187 | closed — item 2vp, commit 4282efa |
| `skills` journey | replacement skill source | The source still contained the old acceptance skill on both isolated attempts. | 187 | closed — item 2vp, commit 4282efa |
| `chat-management` journey | bulk delete while a chat is running | Timed out waiting for bulk delete to name the running chat on both isolated attempts. | 187 | closed — item 2vp, commit 4282efa |
| `compaction` journey | `context.compacted` event | Twelve short exchanges produced no `context.compacted` event on either isolated attempt. | 187 | closed — item 2vp, commit 4282efa |
| `Profiles and Connections share fields, whole numbers, switches, and stable failure layout 2t0` | Playwright settings layout | Passed one of three isolated reruns, fewer than the required two. | 186 | closed — item 2vp, commit 4282efa |
| `Settings geometry follows the six-color console contract 2sy` | Playwright 1920 px geometry | Failed all three isolated reruns at 1920 px. | 187 | closed — item 2vp, commit 4282efa |
| `TestSessionEndIsRecordedBeforeWindowsEndsTheProcess` | hosted Windows all-packages job | The session-end query returned `0` on the original job and the single failed-job rerun. | 188 | closed — item 2vp, commit 4282efa |
| `TestTheSessionEndWindowExistsAsSoonAsTheWatchReturns` | hosted Windows all-packages job | The watcher returned before its guard window existed on the original job and the single failed-job rerun. | 188 | closed — item 2vp, commit 4282efa |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright colour measurement | Ten consecutive isolated runs passed after waiting for the stable current menu state. | 185 | closed — item 2vn |
| `Chats shows only profile reading approval context and delete 2sb` | Playwright menu overlay | Ten consecutive isolated runs passed after waiting for the stable current menu state. | 185 | closed — item 2vn |
| Real-model compaction proof | five serial cases | `fresh` failed conditions 1, 3, 4 at turn 28; `typed-continue` failed 1, 3, 4 at turn 8; `Continue` failed 1, 3 at turn 8; `queued-message` failed 1, 2, 5 at turns 32 and 42; `full` failed 1 and 4 at turn 28. | 192 | moved — item 2wi: a scheduled measurement, not a gate |
| `TestSessionEndIsRecordedBeforeWindowsEndsTheProcess` | hosted Windows all-packages job | First attempt: `lifetime_windows_test.go:98: WM_QUERYENDSESSION answered 0`; `--- FAIL: TestSessionEndIsRecordedBeforeWindowsEndsTheProcess (5.56s)`. [[2vs]]'s fix did not hold on the hosted runner; the single failed-job rerun passed. | 193 | closed — item 2vw, commit 7410fb3 |
| Real-model compaction proof | five serial cases | `fresh` failed condition 1 at turn 123; `queued-message` failed 2 at turn 13; `full` failed 1 and 4 at turn 14; condition 5 held in every case. | 209 | moved — item 2wi: a scheduled measurement, not a gate |
