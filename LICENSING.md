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

## Upstream license clarification

The go-lzo author clarified **GPL-2.0-or-later** in
[issue #15](https://github.com/rasky/go-lzo/issues/15) and
[commit ad21c35](https://github.com/rasky/go-lzo/commit/ad21c35a111113348567708a4db74f998be1a4c3).
That commit directly follows our pinned `96a758eda86e` version and changes only
the README license wording, with no implementation change. This resolves the
previous GPL version-option question and permits choosing GPLv3.

SWD records the clarification in the [license audit](docs/issue-30-license-audit.md)
and preserves the pinned module's original license text. Earlier release
artifacts retain their historical risk records; new releases no longer list
this resolved question as an accepted risk.
