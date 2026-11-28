//go:build winvmlab

package winvm

// labBuild is true only in a lab build (go build -tags winvmlab), which honours
// LabOverrides in the setup params.
const labBuild = true
