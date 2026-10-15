// Package securitypack answers the question a security reviewer asks a team that ships agents:
// how do you control what your AI agents do? (S51). It holds the question bank (24 questions in
// eight groups, in our own words, questions.yaml), the deterministic status rules over evidence,
// the security_pack v1 answers, the free readiness view and the offline check of a pack folder.
//
// Every answer is computed from the org's own facts (ids, counts, hashes, dates and the names of
// its agents, connectors and rules); nothing here reads action arguments, results, row values,
// emails or secrets. A question whose evidence can't be read is Can't tell, never In place. The
// enforced rules are judged by their behaviour: the real evaluator runs fixed probe inputs.
//
// The document renderers, the folder writer, aliases and sealing are Pro (ee/securitypack).
// Licensed Apache-2.0 like the rest of internal/.
package securitypack
