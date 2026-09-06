# OAuth 2.0 sign-in (XOAUTH2)

mailshear can authenticate to IMAP and SMTP with an OAuth 2.0 access token instead of an app password. It ships no OAuth client, so you create one yourself: that is the one-time cost below. Everything after it happens in the app, on the setup form and the accounts screen.

## Google (Gmail, Google Workspace)

1. Create a project at https://console.cloud.google.com/projectcreate (any name).
2. APIs and Services > Library: enable the Gmail API.
3. APIs and Services > OAuth consent screen: User type **External** for a personal account, **Internal** for Workspace.
4. OAuth consent screen > Audience: while the app is in **Testing**, add your own address under Test users.
5. OAuth consent screen > Data access: add the scope `https://mail.google.com/`.
6. APIs and Services > Credentials > Create credentials > OAuth client ID: application type **Desktop app**. A Web application client rejects the loopback redirect mailshear uses.
7. Copy the **Client ID** and the **Client secret** into the setup form. Google issues a secret for a desktop client and its token endpoint requires it, so enter both.

External plus Testing expires refresh tokens after 7 days, so you sign in again weekly until the app is published. Internal (Workspace only) has no such limit.

## Microsoft (Outlook, Hotmail, Microsoft 365)

1. Register an app at https://entra.microsoft.com > Applications > App registrations > New registration (any name).
2. Supported account types: "Personal Microsoft accounts and any organizational directory" for a personal account, "Single tenant" for a work account only.
3. Authentication > Add a platform: **Mobile and desktop applications**, redirect URI `http://localhost`.
4. Authentication > Advanced settings: set **Allow public client flows** to Yes.
5. API permissions > Add a permission > APIs my organization uses > Office 365 Exchange Online > Delegated: add `IMAP.AccessAsUser.All` and `SMTP.Send`.
6. API permissions > Microsoft Graph > Delegated: add `offline_access`.
7. Overview: copy the **Application (client) ID**, and the **Directory (tenant) ID** for a single-tenant app. Do not create a client secret: a public client that sends one is rejected.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| The browser warns the app is unverified | your client is unpublished | Advanced > "Go to ... (unsafe)". It is your own project |
| `invalid_grant` after about a week on Google | External plus Testing expires refresh tokens after 7 days | sign in again from the accounts screen, publish the app, or use an Internal Workspace app |
| `invalid_grant` at any other time | the token was revoked, or the client id changed | sign in again |
| `invalid_client` | wrong client id, or a Google desktop client with no secret entered | check both against the provider's console |
| `unauthorized_client`, or a redirect mismatch on Microsoft | no Mobile and desktop platform, or public client flows are off | add the platform with redirect `http://localhost` and set Allow public client flows to Yes |
| `AADSTS65001` admin consent required | a work or school tenant has not consented | ask the admin to grant consent for `IMAP.AccessAsUser.All` and `SMTP.Send` |
| `does not advertise AUTH=XOAUTH2` | the server has no XOAUTH2, or IMAP is off for the mailbox | enable IMAP, or switch the auth method back to App password |
| `the token was rejected` on Gmail | expired refresh token, or IMAP disabled in Gmail settings | sign in again, and check Gmail > Settings > Forwarding and POP/IMAP |
| Timed out waiting for the browser sign-in | no browser here, or the tab was closed | copy the URL the screen shows into a browser that can reach this machine's loopback address; forward the port on a remote box |
| `refusing to send an access token over an unencrypted connection` | the SMTP host does not offer STARTTLS | check the `smtp:` host and port in the config |

Tokens live at `<data-dir>/credentials/<account>.oauth.json` (mode 0600) and are refreshed when they are within a minute of expiry. Signing out deletes the local token only; revoke access at https://myaccount.google.com/permissions or https://myaccount.microsoft.com/privacy.
