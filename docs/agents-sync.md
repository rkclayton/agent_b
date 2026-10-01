# Agent synchronization contract

The PC is canonical. Nothing is silently merged, substituted, omitted, or resurrected.

| Case | Before → after | Exact user-visible line |
|---|---|---|
| Revision assignment | PC head r4; phone Change `c1` bases r4 → PC writes immutable r5 and echoes `c1 → r5`; both show r5. | `Edit applied as revision 5.` |
| Colliding offline edit | Phone `c2` bases r4 while PC advances to r5 → retain `conflict(head_revision:5)`; chats run on r5 until explicit resolution. | `This edit started from revision 4; revision 5 is current. Choose Keep head, Apply edit over head, or Open both.` |
| Missing baseline text | Chat binds hash `aa…`, phone lacks it → pull exact bytes and verify; neither peer has it → block and name its release. | `Instructions aa… are unavailable on this device. Update, or Continue with another agent.` |
| Deleted agent on offline phone | PC tombstones agent/notes; phone returns with old replicas → tombstones win and nothing returns. | `This agent was deleted on another device and was not restored.` |
| Reused letter | Deleted `agent_e` frees letter `e`, never UUID; PC assigns `e` to a new UUID with empty memory; old chats retain old identity. | `Agent e is new; earlier chats keep their original agent.` |
| Provisional letter | Offline phone creates UUID `x` as `e·`; PC already uses `e` → assign next free `f`, keep UUID, note affected chats. | `Agent e· is now Agent f; its identity did not change.` |
| Missing default | Agent names connection `office`, absent on phone → ask and persist a choice; never substitute or omit. | `Connection “office” is unavailable here. Choose a connection to continue.` |
| Memory merge | PC has `(pc,7)`, phone adds `(phone,3)` → union by `(origin_device,seq)`; matching tombstones remove notes on both. | `Agent memory synchronized.` |

Baseline schema version and content hash are separate checks. Memory follows agent UUID, not revision. Deletion markers and Change ids make replay idempotent.
