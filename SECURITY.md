# Security

## Reporting a vulnerability

Report suspected vulnerabilities privately via GitHub's private vulnerability reporting (Security tab -> "Report a vulnerability") on this repository. Do not open public issues for security reports.

## Release trust chain

Every release is built in CI with goreleaser and ships:

- `checksums.txt` — SHA-256 checksums of all release archives
- `checksums.txt.minisig` — minisign signature over `checksums.txt`
- `*.sbom.json` — a Syft SBOM for each archive

Release signing key (minisign):

```
RWS9SKDBxXVQRL27p1aOVmdoSffl83dqJqKtnwDO6IqEMpdoRf+AMDGL
```

Manual verification:

```bash
minisign -Vm checksums.txt -P RWS9SKDBxXVQRL27p1aOVmdoSffl83dqJqKtnwDO6IqEMpdoRf+AMDGL
sha256sum -c --ignore-missing checksums.txt
```

`scripts/install.sh` performs the same checks automatically; `--require-signature` makes the signature check mandatory.

If the signing key is ever rotated, the new key is announced in the release notes of the first release signed with it, and `scripts/install.sh` plus this file are updated in the same commit.

## Supply-chain hardening

- The proxy container image recipe and base image are digest-pinned; `ws proxy rebuild` refuses to build when the recipe drifts from the pinned known-good state unless `--allow-drift` is passed explicitly.
- CI enforces workflow security posture: SHA-pinned actions, least-privilege permissions, and zizmor static analysis over workflow files.
