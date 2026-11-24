package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"slices"
	"strings"
)

// Both the signup UI and recovery draft use the same order and exact
// spelling. The branch ref is part of the binding, not displayed metadata.
func canonicalNativeOwnerDisplay(target nativeOwnerTarget, organization string, branches []nativeInstallBranch) ([]nativeInstallBranch, bool) {
	if !nativeInstallName(organization) || len(branches) != len(target.Outlets) {
		return nil, false
	}
	branches = slices.Clone(branches)
	slices.SortFunc(branches, func(a, b nativeInstallBranch) int { return strings.Compare(a.Ref, b.Ref) })
	names := make(map[string]bool, len(branches))
	for i, branch := range branches {
		if branch.Ref != target.Outlets[i] || !nativeInstallName(branch.Name) || names[branch.Name] {
			return nil, false
		}
		names[branch.Name] = true
	}
	return branches, true
}

// Version 1 format; field names, order, string bytes and numeric encodings
// are part of the durable contract. Do not reuse browser/storage/wire types.
func nativeOwnerDisplayDigest(actor int64, target nativeOwnerTarget, organization string, branches []nativeInstallBranch) [32]byte {
	type branchV1 struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}
	type displayV1 struct {
		Actor        int64      `json:"actor"`
		Audience     string     `json:"audience"`
		Intent       string     `json:"intent"`
		Business     string     `json:"business"`
		Request      string     `json:"request"`
		Revision     int64      `json:"revision"`
		Organization string     `json:"organization"`
		Branches     []branchV1 `json:"branches"`
	}
	evidence := displayV1{
		Actor: actor, Audience: target.Audience, Intent: target.Intent, Business: target.Business,
		Request: target.RequestRef, Revision: target.Revision, Organization: organization, Branches: make([]branchV1, len(branches)),
	}
	for i, branch := range branches {
		evidence.Branches[i] = branchV1{Ref: branch.Ref, Label: branch.Name}
	}
	encoded, _ := json.Marshal(evidence) // Fixed, JSON-encodable primitive fields only.
	return sha256.Sum256(append([]byte("gomar-native-owner-display-v1\x00"), encoded...))
}

func matchesNativeOwnerDisplay(stored []byte, actor int64, draft nativeInstallDraft) bool {
	expected := nativeOwnerDisplayDigest(actor, draft.Target, draft.DisplayOrganization, draft.Branches)
	return subtle.ConstantTimeCompare(stored, expected[:]) == 1
}
