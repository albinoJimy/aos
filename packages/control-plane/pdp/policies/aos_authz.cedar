// Política de referência do AOS — equivalente Cedar de `package aos.authz`
// (tecnica/12 §9, Rego). Semântica idêntica: DEFAULT-DENY (fail-closed).
//
// Cedar nega por omissão: sem um `permit` explícito aplicável, a decisão é
// `deny`. Cada `permit` abaixo é o equivalente exacto de uma regra
// `allow if { ... }` do Rego. As obrigações (audit / redact_pii) NÃO se
// exprimem em Cedar (a decisão Cedar é apenas allow/deny); são derivadas por
// pós-avaliação determinística coerente com a política (ver engine_cedar.go),
// espelhando as regras `obligations contains { ... } if { allow; ... }`.
//
// Modelo de entidades (mapeamento Input -> request Cedar):
//   principal : Principal  — atributo `authority` : Set<String> (capabilities delegadas)
//   action    : Action     — a capability pedida, ex. Action::"cap:http.post"
//   resource  : Resource   — atributo `region` : String
//   context   : { taint : String, sensitivity : String }

// allow_http_post — equivalente a:
//   allow if {
//     input.capability == "cap:http.post"
//     "cap:http.post" in input.principal.authority
//     input.resource.region == "eu"
//     input.context.taint != "untrusted"
//   }
@id("allow_http_post")
permit (
    principal,
    action == Action::"cap:http.post",
    resource
)
when {
    principal.authority.contains("cap:http.post") &&
    resource.region == "eu" &&
    context.taint != "untrusted"
};

// allow_fs_read — equivalente a:
//   allow if {
//     input.capability == "cap:fs.read"
//     "cap:fs.read" in input.principal.authority
//   }
@id("allow_fs_read")
permit (
    principal,
    action == Action::"cap:fs.read",
    resource
)
when {
    principal.authority.contains("cap:fs.read")
};
