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

// allow_artifact_write — AOS-549 (ADR-043 §2.3; emenda de 2026-10-22 ao ADR-034, decisão do dono):
// gravar no armazém de artefactos do PRÓPRIO run (a tool `artifact_write`: só o armazém do run, sem
// rede, cifrado por titular e apagável pela DSAR) é ESCRITA LOCAL DO RUN e é permitida também com o
// contexto untrusted — é o que deixa um run que viu uma foto gravar a lista dos cômodos. Por isso
// esta regra NÃO tem a cláusula `context.taint != "untrusted"`, e diz porquê na anotação
// `@taint_exempt`, que o gate policy-taint só aceita para as capabilities de escrita local do run
// que ele conhece. A barreira de taint ESTRUTURAL (o TaintGate armado com AOS_PRIVILEGED_CAPS) é a
// que classifica a escrita local do run pelo destino da tool declarado no registo trusted.
@id("allow_artifact_write")
@taint_exempt("local_run_write")
permit (
    principal,
    action == Action::"cap:artifact.write",
    resource
)
when {
    principal.authority.contains("cap:artifact.write")
};
