# Microsoft Entra credentials

Sign in from **Settings → Security → Credentials → Add → Sign-in → Microsoft**. The built-in sign-in is confirmed for Microsoft Graph and Azure management, while Exchange Online remains unconfirmed and may require its own sign-in. The form supplies the Microsoft Azure CLI public client and the resource scope for known addresses. Open **Advanced** to override the tenant, public client ID, or scopes. Microsoft and the tenant determine which delegated permissions the client can obtain.

A chat that shows **Credential needed** opens the relevant Settings form. The same service call continues after the operator signs in. The chat and the tool cannot start or complete sign-in.

## Use a custom public client

Create an app registration in the tenant that owns the target API:

1. Add a **Mobile and desktop applications** platform with the loopback redirect URI `http://localhost`. Agent_b uses an available loopback port for authorization code with PKCE.
2. Treat it as a public client. Do not create or enter a client secret.
3. Add the target API's delegated permissions. Enter those scope values in Agent_b exactly as the API publishes them.
4. If operators may choose device-code sign-in, enable public client flows for the registration. Browser sign-in does not require enabling device code.
5. Grant or consent to those delegated permissions according to the tenant's policy.

The target API must validate the token issuer and audience. Its exposed Application ID URI (or other documented audience) must match the audience the API validates. Authorize the signed-in person with the API's own policy, such as an assigned app role or an allowed group; receiving a valid token is not by itself authorization to every operation.

## Add and sign in

When an MCP endpoint publishes a pre-registered public-client definition in its
protected-resource metadata or unauthenticated `401` body, an agent may include that
public definition in a connector proposal. The approval card shows the exact tenant,
client ID, scopes, credential name, and destination origin. Approval stores the Entra
definition and connector binding together; it never signs in or grants consent. Continue
with **Sign in with browser** in Settings as below.

In **Settings → Security → Credentials**:

1. Open **Add → Sign-in → Microsoft**.
2. Enter a credential name such as `<credential-name>`.
3. Enter the target API origin, for example `https://<api-host>:<port>`. Tokens are attached only to this exact HTTPS scheme, host, and port.
4. Under **Advanced**, enter `<tenant-id>`, `<public-client-id>`, and the space-separated delegated scopes, such as `<api-scope-read> <api-scope-write>`.
5. Save the custom definition.
6. Press **Sign in with browser**. To choose the fallback explicitly, press **Use a device code**, open the shown verification address, and enter the shown code.

Settings shows the sign-in state. **Switch account** replaces that credential's cached account. **Sign out** clears its protected token cache. When silent acquisition fails, the call requests sign-in again. A failure for a second Microsoft resource does not clear the shared account cache.

## Use it from a connector

Set the connector auth reference to `entra:<credential-name>`. The connector's `base_url` origin must equal the origin stored with the credential.

Placeholder example:

```json
{
  "kind": "http",
  "base_url": "https://<api-host>:<port>",
  "auth": "entra:<credential-name>",
  "allowed_methods": ["GET", "POST"]
}
```

An imported OpenAPI document describes operations and parameters, but it never supplies identity configuration. The connector keeps the `entra:` reference, and the API still decides what the signed-in account may do.

## Storage and sign-out

The MSAL refresh/account cache is persisted with the same Windows user-scope DPAPI protection as other Agent_b credentials. Access-token entries are removed before that cache is written and remain in process memory. User-scope DPAPI protects against other Windows users. The separate service-account OS boundary protects the store from a tool process.
