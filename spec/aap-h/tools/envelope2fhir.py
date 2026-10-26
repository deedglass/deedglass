#!/usr/bin/env python3
"""Turns an AAP-H envelope into its FHIR R4 views: an aaph-auditevent, and an aaph-provenance for a
write that ran. A reference sketch of AAP-H section 5.6, standard library only.

  python3 spec/aap-h/tools/envelope2fhir.py examples/nhcx-preauth.envelope.json nhcx-preauth OUTDIR

Licence: Apache-2.0.
"""
import base64
import json
import os
import sys

BASE = "https://deedglass.com/fhir/aap-h"
SD = BASE + "/StructureDefinition/"
CS = BASE + "/CodeSystem/"
BALP = "https://profiles.ihe.net/ITI/BALP/StructureDefinition/"
AIT = "http://hl7.org/fhir/uv/aitransparency/"
WRITES = {"create", "update", "patch", "delete", "operation", "execute"}


def sub(url, kind, value):
    return {"url": url, kind: value}


def ext(name, subs):
    return {"url": SD + name, "extension": [s for s in subs if s is not None]}


def opt(url, kind, value):
    return None if value is None else sub(url, kind, value)


def extensions(e):
    a, p = e["action"], e["policy"]
    out = [ext("aaph-action", [
        sub("actionId", "valueString", a["id"]), sub("tool", "valueString", a["tool"]),
        sub("interaction", "valueCode", a["interaction"]), sub("agentRole", "valueCode", a["agent_role"]),
        sub("systemKind", "valueCode", a["system"]["kind"]), opt("systemBase", "valueUri", a["system"]["base"]),
        sub("requestDigest", "valueString", a["request_digest"]), opt("intentDigest", "valueString", a["intent_digest"])]),
        ext("aaph-policy-decision", [
            sub("decision", "valueCode", p["decision"]), sub("mode", "valueCode", p["mode"]),
            opt("policy", "valueString", p["policy"]), opt("versionDigest", "valueString", p["version_digest"]),
            opt("rule", "valueString", p["rule"])])]
    for ap in e["approvals"]:
        out.append(ext("aaph-approval", [sub("step", "valuePositiveInt", ap["step"]), sub("outcome", "valueCode", ap["outcome"])]
                       + [sub("approver", "valueIdentifier", {"system": "urn:aaph:opaque-ref", "value": r}) for r in ap["approvers"]]
                       + [opt("role", "valueString", ap["role"]), opt("decidedAt", "valueDateTime", ap["decided_at"])]))
    for au in e["authorization"]:
        out.append(ext("aaph-authorization", [sub("kind", "valueCode", au["kind"]), sub("ref", "valueString", au["ref"]),
                                              opt("scope", "valueString", au["scope"]), opt("digest", "valueString", au["digest"])]))
    c = e["claimed"]
    if c:
        out.append(ext("aaph-claimed-outcome", [sub("status", "valueCode", c["status"]), opt("assertion", "valueCode", c["assertion"]),
                                                opt("resultDigest", "valueString", c["result_digest"]), opt("errorCode", "valueString", c["error_code"]),
                                                opt("started", "valueDateTime", c["started_at"]), opt("finished", "valueDateTime", c["finished_at"])]))
    v = e["verified"]
    if v:
        out.append(ext("aaph-verified-outcome", [sub("result", "valueCode", v["result"]), sub("depth", "valueCode", v["depth"]),
                                                 sub("method", "valueString", v["method"]), opt("detailDigest", "valueString", v["detail_digest"]),
                                                 opt("checkedAt", "valueDateTime", v["checked_at"]), opt("supersedes", "valueString", e["supersedes"])]))
    s = e["seal"]
    if s:
        ch = e["chain"] or {}
        out.append(ext("aaph-record-seal", [sub("recordId", "valueString", e["record_id"]), sub("issuer", "valueUri", e["issuer"]),
                                            sub("recordDigest", "valueString", s["record_digest"]), sub("format", "valueCode", s["format"]),
                                            opt("keyId", "valueString", s.get("key_id")), opt("keySet", "valueUri", s.get("key_set")),
                                            opt("stream", "valueString", ch.get("stream")), opt("seq", "valuePositiveInt", ch.get("seq"))]))
    return out


def device(e):
    ag = e["agent"]
    d = {"resourceType": "Device", "id": "ai-agent", "meta": {"profile": [AIT + "StructureDefinition/AI-Device"]},
         "identifier": [{"system": "urn:aaph:agent-id", "value": ag["id"]}],
         "deviceName": [{"name": ag["name"], "type": "user-friendly-name"}],
         "type": {"coding": [{"system": AIT + "CodeSystem/AIdeviceTypeCS", "code": "Artificial-Intelligence"}]}}
    if ag["model"]:
        m = ag["model"]
        d["manufacturer"] = m["provider"]
        d["modelNumber"] = m["name"]
        if m["version"]:
            d["version"] = [{"value": m["version"]}]
    return d


def role(code):
    return [{"coding": [{"system": CS + "aaph-participant", "code": code}]}]


def gateway(e):
    return {"identifier": {"system": "urn:ietf:rfc:3986", "value": e["issuer"]}, "display": "AI agent gateway"}


def approvers(e):
    return [r for ap in e["approvals"] for r in ap["approvers"]]


def sentence(e):
    p, c, v = e["policy"], e["claimed"], e["verified"]
    s = f"Policy {p['decision']}" + (f" by rule {p['rule']}" if p["rule"] else "")
    if e["approvals"]:
        s += "; " + ", ".join(f"step {a['step']} {a['outcome']}" for a in e["approvals"])
    s += "; " + (f"tool reported {c['status']}" if c else "not run")
    if c and c["assertion"]:
        s += f" ({c['assertion']})"
    if v:
        s += f"; check {v['result']} by {v['depth']} ({v['method']})"
    return s + "."


REST = {"create": ("create", "C"), "read": ("read", "R"), "update": ("update", "U"), "patch": ("patch", "U"),
        "delete": ("delete", "D"), "search": ("search-type", "E"), "operation": ("operation", "E"), "execute": (None, "E"), "other": (None, "E")}
BALP_BY = {"create": "IHE.BasicAudit.Create", "read": "IHE.BasicAudit.Read", "update": "IHE.BasicAudit.Update",
           "patch": "IHE.BasicAudit.Update", "delete": "IHE.BasicAudit.Delete", "search": "IHE.BasicAudit.Query"}


def auditevent(e, rid):
    a, c = e["action"], e["claimed"]
    ran_ok = c is not None and c["status"] == "succeeded" and e["policy"]["decision"] != "deny"
    outcome = "0" if ran_ok else ("8" if c is not None and c["status"] == "unknown" else "4")
    profiles = [SD + "aaph-auditevent"]
    if ran_ok and a["system"]["kind"] == "fhir" and a["interaction"] in BALP_BY:
        profiles.append(BALP + BALP_BY[a["interaction"]])
    sub_code, act = REST[a["interaction"]]
    ae = {"resourceType": "AuditEvent", "id": rid, "meta": {"profile": profiles}, "contained": [device(e)],
          "extension": extensions(e)}
    if a["system"]["kind"] == "fhir":
        ae["type"] = {"system": "http://terminology.hl7.org/CodeSystem/audit-event-type", "code": "rest", "display": "Restful Operation"}
        if sub_code:
            ae["subtype"] = [{"system": "http://hl7.org/fhir/restful-interaction", "code": sub_code}]
    else:
        ae["type"] = {"system": "http://dicom.nema.org/resources/ontology/DCM", "code": "110100", "display": "Application Activity"}
    ae["action"] = act
    ae["recorded"] = e["recorded_at"]
    ae["outcome"] = outcome
    ae["outcomeDesc"] = sentence(e)
    agents = [{"type": {"coding": [{"system": "http://dicom.nema.org/resources/ontology/DCM", "code": "110153", "display": "Source Role ID"}]},
               "who": gateway(e), "requestor": False,
               "policy": [au["ref"] for au in e["authorization"] if au["kind"] in ("consent", "grant")] or None},
              {"role": role("ai-agent"), "who": {"reference": "#ai-agent", "display": e["agent"]["name"]}, "requestor": True}]
    if a["system"]["base"]:
        agents.insert(1, {"type": {"coding": [{"system": "http://dicom.nema.org/resources/ontology/DCM", "code": "110152", "display": "Destination Role ID"}]},
                          "who": {"identifier": {"system": "urn:ietf:rfc:3986", "value": a["system"]["base"]}}, "requestor": False})
    for r in approvers(e):
        agents.append({"role": role("approver"), "who": {"identifier": {"system": "urn:aaph:opaque-ref", "value": r}}, "requestor": False})
    for ag in agents:
        if "policy" in ag and ag["policy"] is None:
            del ag["policy"]
    ae["agent"] = agents
    ae["source"] = {"observer": gateway(e), "type": [{"system": "http://terminology.hl7.org/CodeSystem/security-source-type", "code": "4", "display": "Application Server"}]}
    ents = [{"what": {"reference": t["ref"] + (f"/_history/{t['version']}" if t["version"] else "")},
             "type": {"system": "http://terminology.hl7.org/CodeSystem/audit-entity-type", "code": "2", "display": "System Object"},
             "role": {"system": "http://terminology.hl7.org/CodeSystem/object-role", "code": "4", "display": "Domain Resource"}} for t in a["target"]]
    ents.append({"what": {"identifier": {"system": "urn:aaph:record-id", "value": e["record_id"]}},
                 "type": {"system": "http://terminology.hl7.org/CodeSystem/audit-entity-type", "code": "4", "display": "Other"},
                 "detail": [{"type": "request_digest", "valueString": a["request_digest"]}]})
    ae["entity"] = ents
    return ae


def provenance(e, rid):
    a, c = e["action"], e["claimed"]
    if a["agent_role"] != "made" or a["interaction"] not in WRITES or c is None or c["status"] == "failed" or not a["target"]:
        return None
    reason = "CLINAST_AIRPT" if e["approvals"] and any(x["outcome"] == "approved" for x in e["approvals"]) else "AIRPT"
    dataop = {"create": "CREATE", "update": "UPDATE", "patch": "UPDATE", "delete": "DELETE"}.get(a["interaction"])
    ptype = "http://terminology.hl7.org/CodeSystem/provenance-participant-type"
    agents = [{"type": {"coding": [{"system": ptype, "code": "author", "display": "Author"}]}, "role": role("ai-agent"),
               "who": {"reference": "#ai-agent", "display": e["agent"]["name"]}},
              {"type": {"coding": [{"system": ptype, "code": "assembler", "display": "Assembler"}]}, "role": role("gateway"), "who": gateway(e)}]
    if e["agent"]["on_behalf_of"]:
        agents[0]["onBehalfOf"] = {"identifier": {"system": "urn:aaph:opaque-ref", "value": e["agent"]["on_behalf_of"]}}
    v = e["verified"]
    if v and v["result"] == "match" and v["depth"] in ("read-back", "change-log"):
        agents.append({"type": {"coding": [{"system": ptype, "code": "verifier", "display": "Verifier"}]}, "role": role("checker"), "who": gateway(e)})
    for r in approvers(e):
        agents.append({"type": {"coding": [{"system": CS + "aaph-participant", "code": "approver"}]}, "role": role("approver"),
                       "who": {"identifier": {"system": "urn:aaph:opaque-ref", "value": r}}})
    pr = {"resourceType": "Provenance", "id": rid,
          "meta": {"profile": [SD + "aaph-provenance", AIT + "StructureDefinition/AI-Provenance"]},
          "contained": [device(e)], "extension": extensions(e),
          "target": [{"reference": t["ref"] + (f"/_history/{t['version']}" if t["version"] else "")} for t in a["target"]]}
    if c["started_at"] and c["finished_at"]:
        pr["occurredPeriod"] = {"start": c["started_at"], "end": c["finished_at"]}
    pr["recorded"] = e["recorded_at"]
    pol = [au["ref"] for au in e["authorization"] if au["kind"] == "consent"]
    if e["policy"]["policy"]:
        pol.insert(0, "urn:aaph:policy:" + e["policy"]["policy"] + (("#" + e["policy"]["rule"]) if e["policy"]["rule"] else ""))
    if pol:
        pr["policy"] = pol
    pr["reason"] = [{"coding": [{"system": AIT + "CodeSystem/AddedProvenanceCS", "code": reason}]}]
    if dataop:
        pr["activity"] = {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v3-DataOperation", "code": dataop}]}
    pr["agent"] = agents
    pr["entity"] = [{"role": "source", "what": {"identifier": {"system": "urn:aaph:request-digest", "value": a["request_digest"]}}}]
    s = e["seal"]
    if s and s["format"] == "aaph-jcs":
        raw = s["signature"].split(":", 1)[1]
        raw += "=" * (-len(raw) % 4)
        pr["signature"] = [{"type": [{"system": "urn:iso-astm:E1762-95:2013", "code": "1.2.840.10065.1.12.1.14", "display": "Source Signature"}],
                            "when": e["recorded_at"], "who": gateway(e), "targetFormat": "application/json",
                            "data": base64.b64encode(base64.urlsafe_b64decode(raw)).decode()}]
    return pr


def main():
    src, name, outdir = sys.argv[1], sys.argv[2], sys.argv[3]
    e = json.load(open(src, encoding="utf-8"))
    for kind, res in (("AuditEvent", auditevent(e, name)), ("Provenance", provenance(e, name))):
        if res is None:
            continue
        with open(os.path.join(outdir, f"{kind}-{name}.json"), "w", encoding="utf-8") as f:
            json.dump(res, f, indent=2, ensure_ascii=False)
            f.write("\n")


if __name__ == "__main__":
    main()
