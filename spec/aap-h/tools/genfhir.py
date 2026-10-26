#!/usr/bin/env python3
"""Writes the AAP-H FHIR R4 conformance resources into spec/aap-h/fhir/.

Standard library only. Run from anywhere:  python3 spec/aap-h/tools/genfhir.py
The output is deterministic; aaph_test.go checks the files' structure, not this script.
Licence: Apache-2.0 (the script and its output).
"""
import json
import os

BASE = "https://deedglass.com/fhir/aap-h"
VERSION = "0.1.0-draft"
DATE = "2026-10-26"
FHIR = "4.0.1"
PUBLISHER = "Deedglass contributors"
STATUS = "draft"
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "fhir")


def sd_url(name):
    return f"{BASE}/StructureDefinition/{name}"


def cs_url(name):
    return f"{BASE}/CodeSystem/{name}"


def vs_url(name):
    return f"{BASE}/ValueSet/{name}"


COMMON = {
    "version": VERSION,
    "status": STATUS,
    "experimental": True,
    "date": DATE,
    "publisher": PUBLISHER,
    "jurisdiction": [{"coding": [{"system": "http://unstats.un.org/unsd/methods/m49/m49.htm", "code": "001", "display": "World"}]}],
    "copyright": "Apache-2.0. Part of Agent Action Provenance for Health (AAP-H) v0.1 draft.",
}

# --- code systems -------------------------------------------------------------------------------

CODESYSTEMS = {
    "aaph-verification-result": ("Verification result", "What a check of the system of record found after an action.", [
        ("match", "Match", "The system of record shows what was claimed."),
        ("mismatch", "Doesn't match", "The system of record shows something other than what was claimed, or nothing."),
        ("cannot-check", "Can't check", "No conclusion: no read access, the read failed, the system can't be read back, or the check found a gap."),
    ]),
    "aaph-check-depth": ("Check depth", "How a verified outcome was obtained.", [
        ("reported", "Reported", "Only the tool's own answer."),
        ("read-back", "Read back", "A separate read of the system of record after the action."),
        ("change-log", "Change log", "The system's own change stream or history, bound to this action."),
    ]),
    "aaph-policy-decision": ("Policy decision", "What the policy decided for an action.", [
        ("allow", "Allow", "The action may run."),
        ("deny", "Deny", "The action was refused and did not run."),
        ("require-approval", "Require approval", "The action waits for one or more people to approve it."),
    ]),
    "aaph-approval-outcome": ("Approval outcome", "How one approval step ended.", [
        ("approved", "Approved", "The approvers approved the action."),
        ("denied", "Denied", "An approver refused the action."),
        ("expired", "Expired", "Nobody decided in time."),
        ("cancelled", "Cancelled", "The request for approval was withdrawn."),
    ]),
    "aaph-claimed-status": ("Claimed status", "What the tool's response or the agent said happened.", [
        ("succeeded", "Succeeded", "The tool or agent reported success."),
        ("failed", "Failed", "The tool reported failure, and the action certainly did not apply."),
        ("unknown", "Unknown", "It can't be known whether the action applied."),
    ]),
    "aaph-agent-role": ("Agent role", "Whether the AI agent made the call or only proposed it.", [
        ("proposed", "Proposed", "The agent drafted the action; a person or another system made the call."),
        ("made", "Made", "The agent's call was executed."),
    ]),
    "aaph-interaction": ("Interaction", "The kind of call.", [
        ("create", "Create", "Creates a record."),
        ("read", "Read", "Reads one record."),
        ("update", "Update", "Replaces a record."),
        ("patch", "Patch", "Changes part of a record."),
        ("delete", "Delete", "Deletes a record."),
        ("search", "Search", "Searches records."),
        ("operation", "Operation", "A named operation such as Claim/$submit."),
        ("execute", "Execute", "Runs a statement or command that may change data."),
        ("other", "Other", "Anything else."),
    ]),
    "aaph-system-kind": ("System kind", "The kind of system of record.", [
        ("fhir", "FHIR server", "A FHIR server or FHIR API."),
        ("postgres", "PostgreSQL", "A PostgreSQL database."),
        ("saas", "SaaS API", "A hosted application's API."),
        ("other", "Other", "Anything else."),
    ]),
    "aaph-authorization-kind": ("Authorization kind", "What allowed the agent to act.", [
        ("smart-scope", "SMART scope", "SMART on FHIR scopes granted to the client."),
        ("consent", "Consent", "A patient consent or consent artefact, such as an ABDM consent artefact."),
        ("grant", "Grant", "Another grant: a role, a delegation, a contract."),
        ("other", "Other", "Anything else."),
    ]),
    "aaph-participant": ("Participant", "Roles in an AI agent action.", [
        ("ai-agent", "AI agent", "Software that chose the call using a machine-learning model."),
        ("gateway", "Gateway", "The component that applied the policy, ran or forwarded the call and wrote the record."),
        ("approver", "Approver", "A person who approved or refused the action before it ran."),
        ("checker", "Checker", "The component that checked the system of record after the action."),
    ]),
    "aaph-seal-format": ("Seal format", "How a record is signed.", [
        ("aaph-jcs", "AAP-H JCS", "The envelope signs itself (AAP-H section 8.2)."),
        ("deedglass-deed", "Deedglass deed", "A Deedglass deed the envelope is derived from (AAP-H section 8.3)."),
        ("other", "Other", "Another registered native format."),
    ]),
}


def codesystem(name, title, desc, concepts):
    cs = {
        "resourceType": "CodeSystem",
        "id": name,
        "url": cs_url(name),
        "name": "".join(p.capitalize() for p in name.split("-")),
        "title": f"AAP-H {title}",
        **COMMON,
        "description": desc,
        "caseSensitive": True,
        "valueSet": vs_url(name),
        "content": "complete",
        "count": len(concepts),
        "concept": [{"code": c, "display": d, "definition": df} for c, d, df in concepts],
    }
    return cs


def valueset(name, title, desc):
    return {
        "resourceType": "ValueSet",
        "id": name,
        "url": vs_url(name),
        "name": "".join(p.capitalize() for p in name.split("-")) + "VS",
        "title": f"AAP-H {title}",
        **COMMON,
        "description": f"All codes of {cs_url(name)}. {desc}",
        "compose": {"include": [{"system": cs_url(name)}]},
    }


# --- extensions ---------------------------------------------------------------------------------

# (sub name, FHIR type(s), min, max, short, binding value set or None)
EXTENSIONS = {
    "aaph-action": ("The AI agent's call", "What the AI agent called, on which system, and the digest of what it sent.", [
        ("actionId", ["string"], 1, "1", "The action's id at the gateway", None),
        ("tool", ["string"], 1, "1", "The tool name, such as fhir.create or postgres.execute", None),
        ("interaction", ["code"], 1, "1", "The kind of call", "aaph-interaction"),
        ("agentRole", ["code"], 1, "1", "Whether the agent made the call or only proposed it", "aaph-agent-role"),
        ("systemKind", ["code"], 1, "1", "The kind of system of record", "aaph-system-kind"),
        ("systemBase", ["uri"], 0, "1", "The system's base URL", None),
        ("requestDigest", ["string"], 1, "1", "sha256: digest of the JCS form of the request", None),
        ("intentDigest", ["string"], 0, "1", "sha256: digest of a signed intent, when the agent signed one", None),
    ]),
    "aaph-policy-decision": ("Policy decision", "Which policy rule decided the action, and how.", [
        ("decision", ["code"], 1, "1", "allow, deny or require-approval", "aaph-policy-decision"),
        ("mode", ["code"], 1, "1", "enforce or shadow", None),
        ("policy", ["string"], 0, "1", "The policy's id", None),
        ("versionDigest", ["string"], 0, "1", "sha256: digest of the policy version", None),
        ("rule", ["string"], 0, "1", "The rule that decided", None),
    ]),
    "aaph-approval": ("Approval step", "One step of the approval chain.", [
        ("step", ["positiveInt"], 1, "1", "The step number, from 1", None),
        ("outcome", ["code"], 1, "1", "approved, denied, expired or cancelled", "aaph-approval-outcome"),
        ("approver", ["Reference", "Identifier"], 0, "*", "Who decided (opaque references preferred)", None),
        ("role", ["string"], 0, "1", "The role the step required", None),
        ("decidedAt", ["dateTime"], 0, "1", "When the step was decided", None),
    ]),
    "aaph-authorization": ("Authorization", "What allowed the agent to act: SMART scopes, a consent artefact, a grant.", [
        ("kind", ["code"], 1, "1", "smart-scope, consent, grant or other", "aaph-authorization-kind"),
        ("ref", ["string"], 1, "1", "The client id, consent artefact id or grant reference", None),
        ("scope", ["string"], 0, "1", "Scopes, space separated", None),
        ("digest", ["string"], 0, "1", "sha256: digest of the signed artefact", None),
    ]),
    "aaph-claimed-outcome": ("Claimed outcome", "What the tool's response or the agent said happened.", [
        ("status", ["code"], 1, "1", "succeeded, failed or unknown", "aaph-claimed-status"),
        ("assertion", ["code"], 0, "1", "A short code for the effect the agent claimed, such as payer-approved", None),
        ("resultDigest", ["string"], 0, "1", "sha256: digest of the tool's response", None),
        ("errorCode", ["string"], 0, "1", "The error code the tool returned", None),
        ("started", ["dateTime"], 0, "1", "When the call started", None),
        ("finished", ["dateTime"], 0, "1", "When the call finished", None),
    ]),
    "aaph-verified-outcome": ("Verified outcome", "What a check of the system of record found after the action.", [
        ("result", ["code"], 1, "1", "match, mismatch or cannot-check", "aaph-verification-result"),
        ("depth", ["code"], 1, "1", "reported, read-back or change-log", "aaph-check-depth"),
        ("method", ["string"], 1, "1", "How the check was made, such as readback.fhir.create", None),
        ("detailDigest", ["string"], 0, "1", "sha256: digest of the check's detail", None),
        ("checkedAt", ["dateTime"], 0, "1", "When the check finished", None),
        ("supersedes", ["string"], 0, "1", "The record whose verified outcome this replaces", None),
    ]),
    "aaph-record-seal": ("Record seal", "Points to the signed record this resource is a view of.", [
        ("recordId", ["string"], 1, "1", "The record's id", None),
        ("issuer", ["uri"], 1, "1", "The issuer's base URI", None),
        ("recordDigest", ["string"], 1, "1", "sha256: record digest (AAP-H section 8)", None),
        ("format", ["code"], 1, "1", "aaph-jcs, deedglass-deed or other", "aaph-seal-format"),
        ("keyId", ["string"], 0, "1", "The signing key's id", None),
        ("keySet", ["uri"], 0, "1", "Where the issuer publishes its key set", None),
        ("stream", ["string"], 0, "1", "The record's stream", None),
        ("seq", ["positiveInt"], 0, "1", "The record's position in its stream", None),
    ]),
}


def extension_sd(name, title, desc, subs):
    url = sd_url(name)
    elems = [
        {"id": "Extension", "path": "Extension", "short": title, "definition": desc},
        {"id": "Extension.extension", "path": "Extension.extension", "slicing": {"discriminator": [{"type": "value", "path": "url"}], "rules": "closed"}},
    ]
    for sub, types, mn, mx, short, vs in subs:
        sid = f"Extension.extension:{sub}"
        elems.append({"id": sid, "path": "Extension.extension", "sliceName": sub, "short": short, "definition": short + ".", "min": mn, "max": mx})
        elems.append({"id": f"{sid}.extension", "path": "Extension.extension.extension", "max": "0"})
        elems.append({"id": f"{sid}.url", "path": "Extension.extension.url", "fixedUri": sub})
        ve = {"id": f"{sid}.value[x]", "path": "Extension.extension.value[x]", "min": 1, "type": [{"code": t} for t in types]}
        if vs:
            ve["binding"] = {"strength": "required", "valueSet": vs_url(vs)}
        if sub == "mode" and name == "aaph-policy-decision":
            ve["binding"] = {"strength": "required", "valueSet": vs_url("aaph-policy-mode")}
        elems.append(ve)
    elems.append({"id": "Extension.url", "path": "Extension.url", "fixedUri": url})
    elems.append({"id": "Extension.value[x]", "path": "Extension.value[x]", "max": "0"})
    return {
        "resourceType": "StructureDefinition",
        "id": name,
        "url": url,
        "name": "".join(p.capitalize() for p in name.split("-")),
        "title": f"AAP-H {title}",
        **COMMON,
        "description": desc,
        "fhirVersion": FHIR,
        "kind": "complex-type",
        "abstract": False,
        "context": [{"type": "element", "expression": "Provenance"}, {"type": "element", "expression": "AuditEvent"}],
        "type": "Extension",
        "baseDefinition": "http://hl7.org/fhir/StructureDefinition/Extension",
        "derivation": "constraint",
        "differential": {"element": elems},
    }


# --- profiles -----------------------------------------------------------------------------------

EXT_SLICES = [
    ("action", "aaph-action", 1, "1"),
    ("policyDecision", "aaph-policy-decision", 1, "1"),
    ("approval", "aaph-approval", 0, "*"),
    ("authorization", "aaph-authorization", 0, "*"),
    ("claimedOutcome", "aaph-claimed-outcome", 0, "1"),
    ("verifiedOutcome", "aaph-verified-outcome", 0, "*"),
    ("recordSeal", "aaph-record-seal", 0, "1"),
]

WRITES = "('create' | 'update' | 'patch' | 'delete' | 'operation' | 'execute')"


def invariants(root):
    a = sd_url("aaph-action")
    p = sd_url("aaph-policy-decision")
    ap = sd_url("aaph-approval")
    c = sd_url("aaph-claimed-outcome")
    v = sd_url("aaph-verified-outcome")
    inter = f"extension('{a}').extension('interaction').value"
    out = [
        {"key": "aaph-1", "severity": "error", "human": "A decision of require-approval has at least one approval step.",
         "expression": f"extension('{p}').extension('decision').value = 'require-approval' implies extension('{ap}').exists()"},
        {"key": "aaph-2", "severity": "error", "human": "A write is never recorded as a match at depth reported.",
         "expression": f"{inter} in {WRITES} implies extension('{v}').where(extension('result').value = 'match' and extension('depth').value = 'reported').empty()"},
        {"key": "aaph-3", "severity": "warning", "human": "A write the tool reported as succeeded has a verified outcome.",
         "expression": f"({inter} in {WRITES} and extension('{c}').extension('status').value = 'succeeded') implies extension('{v}').exists()"},
    ]
    if root == "Provenance":
        out.append({"key": "aaph-4", "severity": "error", "human": "Exactly one agent is the AI agent, and it is a Device.",
                    "expression": f"agent.where(role.coding.where(system = '{cs_url('aaph-participant')}' and code = 'ai-agent').exists()).count() = 1"})
    return out


def coding(system, code, display=None):
    c = {"system": system, "code": code}
    if display:
        c["display"] = display
    return c


def profile(root):
    name = "aaph-provenance" if root == "Provenance" else "aaph-auditevent"
    title = "AI agent action Provenance" if root == "Provenance" else "AI agent action AuditEvent"
    desc = ("The provenance of a resource an AI agent created, updated or deleted: the agent, the policy decision, the approval chain, "
            "the claimed and verified outcomes, and the signed record." if root == "Provenance" else
            "The audit record of one AI agent action, whatever its outcome, including refused calls: the agent, the policy decision, "
            "the approval chain, the claimed and verified outcomes, and the signed record. Claims the matching IHE BALP profile when the call was a successful RESTful interaction.")
    elems = [{"id": root, "path": root, "short": title, "definition": desc, "constraint": [
        {**inv, "source": sd_url(name)} for inv in invariants(root)]}]
    elems.append({"id": f"{root}.extension", "path": f"{root}.extension", "slicing": {"discriminator": [{"type": "value", "path": "url"}], "rules": "open"}, "min": 2})
    for slice_name, ext, mn, mx in EXT_SLICES:
        elems.append({"id": f"{root}.extension:{slice_name}", "path": f"{root}.extension", "sliceName": slice_name, "min": mn, "max": mx,
                      "type": [{"code": "Extension", "profile": [sd_url(ext)]}]})
    part = cs_url("aaph-participant")
    if root == "Provenance":
        ptype = "http://terminology.hl7.org/CodeSystem/provenance-participant-type"
        elems.append({"id": "Provenance.agent", "path": "Provenance.agent", "slicing": {"discriminator": [{"type": "pattern", "path": "role"}], "rules": "open"}})
        for sl, mn, mx, typ, role, who in [
            ("aiAgent", 1, "1", coding(ptype, "author", "Author"), "ai-agent", ["http://hl7.org/fhir/StructureDefinition/Device"]),
            ("gateway", 0, "1", coding(ptype, "assembler", "Assembler"), "gateway", None),
            ("checker", 0, "*", coding(ptype, "verifier", "Verifier"), "checker", None),
            ("approver", 0, "*", coding(part, "approver", "Approver"), "approver", None),
        ]:
            sid = f"Provenance.agent:{sl}"
            elems.append({"id": sid, "path": "Provenance.agent", "sliceName": sl, "min": mn, "max": mx})
            elems.append({"id": f"{sid}.type", "path": "Provenance.agent.type", "min": 1, "patternCodeableConcept": {"coding": [typ]}})
            elems.append({"id": f"{sid}.role", "path": "Provenance.agent.role", "min": 1, "patternCodeableConcept": {"coding": [coding(part, role)]}})
            if who:
                elems.append({"id": f"{sid}.who", "path": "Provenance.agent.who", "type": [{"code": "Reference", "targetProfile": who}]})
        elems.append({"id": "Provenance.signature", "path": "Provenance.signature", "short": "The record's signature (AAP-H section 8.4)",
                      "comment": "The bytes sign the sealed record's digest, not this Provenance resource."})
    else:
        elems.append({"id": "AuditEvent.agent", "path": "AuditEvent.agent", "slicing": {"discriminator": [{"type": "pattern", "path": "role"}], "rules": "open"}})
        for sl, mn, mx, role, req, who in [
            ("aiAgent", 1, "1", "ai-agent", True, ["http://hl7.org/fhir/StructureDefinition/Device"]),
            ("approver", 0, "*", "approver", False, None),
        ]:
            sid = f"AuditEvent.agent:{sl}"
            elems.append({"id": sid, "path": "AuditEvent.agent", "sliceName": sl, "min": mn, "max": mx})
            elems.append({"id": f"{sid}.role", "path": "AuditEvent.agent.role", "min": 1, "patternCodeableConcept": {"coding": [coding(part, role)]}})
            if who:
                elems.append({"id": f"{sid}.who", "path": "AuditEvent.agent.who", "min": 1, "type": [{"code": "Reference", "targetProfile": who}]})
            elems.append({"id": f"{sid}.requestor", "path": "AuditEvent.agent.requestor", "fixedBoolean": req})
    return {
        "resourceType": "StructureDefinition",
        "id": name,
        "url": sd_url(name),
        "name": "".join(p.capitalize() for p in name.split("-")),
        "title": f"AAP-H {title}",
        **COMMON,
        "description": desc,
        "fhirVersion": FHIR,
        "kind": "resource",
        "abstract": False,
        "type": root,
        "baseDefinition": f"http://hl7.org/fhir/StructureDefinition/{root}",
        "derivation": "constraint",
        "differential": {"element": elems},
    }


def write(res):
    path = os.path.join(OUT, f"{res['resourceType']}-{res['id']}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(res, f, indent=2, ensure_ascii=False)
        f.write("\n")


def main():
    os.makedirs(OUT, exist_ok=True)
    for name, (title, desc, concepts) in CODESYSTEMS.items():
        write(codesystem(name, title, desc, concepts))
        write(valueset(name, title, desc))
    write(codesystem("aaph-policy-mode", "Policy mode", "Whether a decision was enforced or only recorded.", [
        ("enforce", "Enforce", "The decision was applied."), ("shadow", "Shadow", "The decision was recorded but not applied.")]))
    write(valueset("aaph-policy-mode", "Policy mode", "Whether a decision was enforced or only recorded."))
    for name, (title, desc, subs) in EXTENSIONS.items():
        write(extension_sd(name, title, desc, subs))
    write(profile("Provenance"))
    write(profile("AuditEvent"))


if __name__ == "__main__":
    main()
