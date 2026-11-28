//go:build !winvmlab

package winvm

// labBuild is false in every product build: setup refuses lab settings, so a
// product build never lowers the installed-RAM floor, keeps Enhanced Session
// on, or builds under another instance's names.
const labBuild = false
