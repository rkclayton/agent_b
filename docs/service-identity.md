# Service identity end-to-end check

The expected columns are the product contract. The measured columns stay explicit: no qualifying clean Windows bed was available for this build, so none of these rows is claimed as an end-to-end pass.

| # | Chat operation | Expected off | Expected on | Measured off | Measured on |
|---:|---|---|---|---|---|
| 1 | `whoami` | launching user | account ending `\agentb-svc` | not run — no bed | not run — no bed |
| 2 | create, edit, read and delete in the chat folder | done | done | not run — no bed | not run — no bed |
| 3 | attach and read a file | done | done | not run — no bed | not run — no bed |
| 4 | deliver a made file to exchange | done | done | not run — no bed | not run — no bed |
| 5 | `remember`, then `recall` | done | done | not run — no bed | not run — no bed |
| 6 | `fetch_url` and `web_search` against the stand-in | done | done | not run — no bed | not run — no bed |
| 7 | delegated helper reads a chat file | done | done | not run — no bed | not run — no bed |
| 8 | write and read a plan | done | done | not run — no bed | not run — no bed |
| 9 | read the launching user's Documents file | approve folder, then done | Windows refuses; Run as you is offered; Just once works once; Keep denied stays refused | not run — no bed | not run — no bed |
| 10 | write under the installed application | approve folder, then done | Windows refuses | not run — no bed | not run — no bed |
| 11 | read configuration, credential, log and another chat's journal | approve folder, then done | Windows refuses | not run — no bed | not run — no bed |
| 12 | shell connects to a public address | connects | blocked | not run — no bed | not run — no bed |
| 13 | shell connects to the model address | connects | connects | not run — no bed | not run — no bed |
| 14 | program outside Windows directories by name, then full path | runs; runs | not found; runs | not run — no bed | not run — no bed |
| 15 | PowerShell `run_script` | runs without an identity question | asks, then runs as `agentb-svc` | not run — no bed | not run — no bed |
| 16 | command past its time limit | complete process tree ends | complete process tree ends | not run — no bed | not run — no bed |
| 17 | SSH using the launching user's key | connects | no key is available | not run — no bed | not run — no bed |

## Spare-machine script

1. Start from a clean, disposable Windows installation and sign in as an ordinary, non-administrator user. Confirm no `agentb-svc` account and no `AgentB-Svc-Outbound-Block` rule exist.
2. Run the released `Agent_b-setup.exe` without elevation. Confirm Agent_b itself is not elevated.
3. In first-run Setup, save and test a stand-in model at numeric loopback or a numeric address in the RFC 6598 shared range.
4. Leave the service-identity switch off, finish Setup, run rows 1–17, and record the exact result in the measured-off column.
5. Reinstall on a clean snapshot. Turn the service-identity switch on, grant its one Windows elevation, and require `ready` before Next becomes available.
6. Finish Setup. Run rows 1–17 and record the exact result in the measured-on column. Row 1 must end in `\agentb-svc`.
7. Restart Agent_b and repeat rows 1–8. The identity must remain on and ready without another elevation.
8. Turn the identity off, run `whoami`, restart, and run it again. Both results must name the launching user and no elevation may appear.
9. Turn the ready identity on, run `whoami`, restart, and run it again. Both results must end in `\agentb-svc` and no elevation may appear. Repeat the off/on round three times.
10. While a chat is running, press the switch. It must refuse the change and no later command in that run may use a different identity.
11. Enable **Run everything as me for 20 minutes**, prove row 1 names the launching user, let the grant expire, and prove row 1 again ends in `\agentb-svc`.
12. Inspect Windows directly: the service account is not an Administrator, the SID-scoped firewall rule exists, and the application, data, chat, plan, scratch, workspace and exchange ACL results match the Security page.
13. Uninstall and remove the disposable VM. Do not reuse a machine which contains a person's real Agent_b installation or service identity.
