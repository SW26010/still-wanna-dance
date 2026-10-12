# Software licensing

Copyright (c) 2026 Still Wanna Dance contributors.

SWD's new original contributions from this change are licensed under
**GPL-3.0-only**. The current combined SWD distribution is offered under GNU GPL
version 3; the full, unmodified license is in [LICENSE](LICENSE). The project does
not grant an option to use later GPL versions for its new original contributions.

SWD code previously distributed through commit
`5f215b2a24e952e1b041a1270c9823c58cba3892` was offered under MIT. Those grants
remain valid; [LICENSE-MIT](LICENSE-MIT) preserves the original copyright and
permission notice. Combining that code into a GPLv3 distribution does not revoke
the original MIT permissions. Do not remove those notices from copies or
substantial portions of the earlier code.

Third-party code, embedded assets and dependency source retain their own licenses
and copyright notices. GPL-3.0-only is not a claim to relicense every dependency.
See [THIRD-PARTY-NOTICES.txt](THIRD-PARTY-NOTICES.txt) and the
[versioned audit](docs/issue-30-license-audit.md). Software licensing does not grant
rights to third-party music, video, services, names or trademarks.

Official online binary releases must provide the matching complete corresponding
source next to the Portable download, without an additional fee or private
credentials. The Portable package need not contain the source tree. Preserve the
source and notices for as long as the associated binaries are offered. A mirror
or offline distributor must review the source-delivery requirements for its own
channel, rather than assuming a link on our Release page covers its distribution.

The usage/content notice is not an additional condition on GPL rights. Recipients
retain the rights to run, study, modify, distribute and commercially use the
software under its applicable licenses, including changing the supplied UI.

## Accepted upstream risk

On 2026-10-12 the maintainer explicitly accepted the unresolved version-option
declaration of `github.com/rasky/go-lzo` at
`v0.0.0-20200203143853-96a758eda86e` as a **non-blocking risk**. This is a risk
decision, not proof of GPLv3 compatibility or a grant of rights by an upstream
author. Its presence alone does not prevent a release. Continue using official
Mihomo; no fork, engine replacement or wait for an author response is required.

If an actual GPLv2-only conflict is later confirmed, stop distribution of affected
builds, assess earlier distribution and pursue an upstream correction. Required
licenses, notices, corresponding source and version checks remain release gates.
