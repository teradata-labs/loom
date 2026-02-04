# Release Notes

This directory contains release notes for all Loom versions, organized for [OSSF (Open Source Security Foundation)](https://openssf.org/) compliance.

## Structure

Each release has a dedicated markdown file:
- `v1.1.0.md` - Release 1.1.0 notes
- `v1.0.2.md` - Release 1.0.2 notes
- etc.

## Format

Each release note includes:
- **Release Status**: Current state of the release
- **Summary**: Overview of changes
- **Breaking Changes**: API changes requiring user action
- **Added Features**: New functionality
- **Bug Fixes**: Issues resolved
- **Security Updates**: CVE fixes and security improvements
- **Dependencies**: Version updates
- **Manual Steps**: Actions required for release
- **Verification**: How to verify the release

## Security

All releases starting with v1.1.0 include:
- **GPG Signatures**: Checksums signed with GPG key
- **SLSA Provenance**: Build attestations for supply chain security

To verify releases, see the verification instructions in each release note.

## Current Release

**Latest**: [v1.1.0](v1.1.0.md)

## Past Releases

- **v1.0.2** (2026-01-15) - Package distribution fixes - See CHANGELOG.md
- **v1.0.1** (2026-01-14) - Database migration fixes - See CHANGELOG.md
- **v1.0.0** (2026-01-09) - Initial release - See CHANGELOG.md

## Contributing

When creating a new release:
1. Copy the previous release note as a template
2. Update version numbers and dates
3. Document all changes, especially breaking changes
4. Include verification instructions
5. Commit to this directory: `RELEASE_NOTES/vX.Y.Z.md`
