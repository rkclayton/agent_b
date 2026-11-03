# Persistent PowerShell

An agent profile may set **persistent PowerShell** to **Off**, **Read only**, or **Full** in Settings → Profiles. Off is the default and removes `powershell_session` from that agent's tools. Read only and Full start one hidden, non-interactive PowerShell process for each session name on first use. Variables, imported modules, and authenticated module state remain until the chat closes, the command reaches 120 seconds, or the session is idle for 30 minutes.

Session names come from `<profile>\powershell\<name>.ps1`. `default` always exists and needs no file. A named file is its bootstrap and runs once when that session opens. For example, `<profile>\powershell\graph.ps1` may contain:

```powershell
# credentials: graph
Connect-MgGraph -AccessToken (ConvertTo-SecureString $AgentB.graph -AsPlainText -Force)
```

The first line may list lower-case credential names separated by commas. Agent_b reads keys from its protected credential store and obtains Entra access tokens silently through the existing signed-in account. Each value is set as `$AgentB.<name>` before the rest of the bootstrap runs. Bootstrap text and values are not placed in the chat, tool result, or journal. A missing credential or one that needs sign-in returns `auth_error: sign in in Settings → Security → Credentials`.

The access token must be for the module's own resource. A Graph token cannot authenticate Exchange Online. An Exchange bootstrap uses its separately configured credential and the parameters required by that module, for example:

```powershell
# credentials: exchange
Connect-ExchangeOnline -AccessToken $AgentB.exchange -UserPrincipalName <user-principal-name>
```

Read only parses the complete command inside that same runspace before executing any of it. It walks nested script blocks and pipelines, resolves aliases, and allows read verbs plus the small output/import exception list on the Rules page. Dynamic command names, `Invoke-*`, native programs, .NET method calls, redirection, environment writes, and commands with non-read verbs are refused in one line. Full skips that check; it remains governed by the same Windows identity, ACL, and outbound-firewall posture as `shell`.
