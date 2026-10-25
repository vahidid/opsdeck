package update

// releaseKey2026 signs opsdeck releases from 0.4.0 on. Its private half is
// the GitHub Actions secret OPSDECK_SIGNING_KEY (docs/releasing.md). Verify a
// download with: minisign -Vm SHA256SUMS.txt -P RWRns3uLxhaLFYGyA2gOUMh1YqtebJGtbVhNL1qWwsaoi2g6Mk6g4+XN
const releaseKey2026 = "RWRns3uLxhaLFYGyA2gOUMh1YqtebJGtbVhNL1qWwsaoi2g6Mk6g4+XN"
