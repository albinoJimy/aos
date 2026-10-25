// POLÍTICA 1.2.0 — CANDIDATA, POR ASSINAR (AOS-556, ADR-044)
//
// ESTE FICHEIRO NÃO ESTÁ EM VIGOR e não é lido por nenhum nó. É o texto que substitui o
// `packages/control-plane/pdp/policies/aos_authz.cedar` na cerimónia da chave do dono. Vive aqui,
// e não lá, porque pôr este texto no bundle sem a assinatura nova faz o `content_hash` recomputado
// deixar de bater com o manifesto: o PDP recusa o bundle fail-closed e nenhum nó arranca com
// política.
//
// O QUE O VALIDA ENTRETANTO: `TestAOS556_PoliticaCandidataDecidePorDestinoEProveniencia` em
// `packages/control-plane/pdp` assina-o numa pasta temporária com uma chave descartável e prova as
// decisões, par allow+deny por regra; e o self-test do gate `policy-taint` corre o gate sobre ele.
// Nenhum dos dois toca no bundle assinado nem na âncora.
//
// A CERIMÓNIA, quando o dono a fizer:
//
//   cp docs/governance/politica-1.2.0-candidata.cedar \
//      packages/control-plane/pdp/policies/aos_authz.cedar
//   cd packages/control-plane/pdp && go run ./cmd/policy-sign -version 1.2.0
//
// Depois: remover a entrada `allow_fs_read` de `scripts/ci/baseline/policy-taint.txt` (fica
// OBSOLETA, e o gate falha até ser removida — a baseline só encolhe, e esta é a última entrada);
// mover os pares allow+deny das regras novas para `ruleCoverageTable` em `policy_suite_test.go`; e,
// se a chave mudar, rodar o `trust_anchor.pub`, os seis literais da âncora em
// `packages/integration` e o `AOS_POLICY_TRUST_ANCHOR` do `.env` do servidor.
//
// -----------------------------------------------------------------------------------------------
//
// Política de referência do AOS — equivalente Cedar de `package aos.authz`
// (tecnica/12 §9, Rego). Semântica idêntica: DEFAULT-DENY (fail-closed).
//
// Modelo de entidades (mapeamento Input -> request Cedar):
//   principal : Principal  — atributo `authority` : Set<String> (capabilities delegadas)
//   action    : Action     — a capability pedida, ex. Action::"cap:http.post"
//   resource  : Resource   — atributo `region` : String
//   context   : { taint : String, sensitivity : String, destination : String, provenance : String }
//
// Os dois atributos novos do `context` (AOS-556, ADR-044 §2.2 e §2.3) são derivados pelo nó e
// nunca pelo modelo: `destination` é a classe que o registo de tools DECLARA, e `provenance` diz de
// onde vem o untrusted desta call (`clean`, `handle_argument` ou `context`, com `context` a
// dominar).

// allow_http_post — INALTERADA face à 1.1.0, e é deliberado (ADR-044 §2.7). Nenhuma proveniência
// satisfaz a cláusula de taint, pelo que um handle untrusted NUNCA flui para um efeito. É a
// assimetria que a fase existe para criar, imposta pela ausência de excepção.
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

// allow_fs_read_local — A PRIMEIRA METADE DA `allow_fs_read` PARTIDA (ADR-044 §2.6, decisão do
// dono de 2026-10-25).
//
// Uma leitura que não sai nem muda nada é permitida TAMBÉM com o contexto untrusted: é a emenda de
// 2026-10-19 ao ADR-034 («depois de ver algo de fora, o agente pode ler à vontade; só não pode
// enviar nem mudar nada»), que até aqui vivia só na classificação estrutural do TaintGate. Passa a
// estar na política ASSINADA, onde quem assina a vê — que é a razão pela qual a `@taint_exempt`
// existe em vez de uma entrada de baseline.
//
// O DESTINO VEM DO REGISTO TRUSTED do operador, com os três eixos EXPLÍCITOS. Um eixo implícito dá
// `unknown` e não satisfaz esta regra nem a seguinte: a leitura fica negada até o manifesto a
// declarar. É aperto deliberado, e é o que torna a declaração dos três eixos uma pré-condição da
// permissão em vez de uma boa prática.
@id("allow_fs_read_local")
@taint_exempt("local_read")
permit (
    principal,
    action == Action::"cap:fs.read",
    resource
)
when {
    principal.authority.contains("cap:fs.read") &&
    context.destination == "local_read"
};

// allow_fs_read_external — A SEGUNDA METADE, E A REGRA DE FLUXO (ADR-044 §2.6).
//
// Uma leitura cujo conteúdo SAI para um terceiro que não muda nada no mundo — a `ocr_read` e a
// `video_read`, que mandam a media a um fornecedor de modelo e recebem texto — é permitida com o
// contexto limpo, e com o contexto untrusted SÓ quando a proveniência é `handle_argument`: isto é,
// quando o que suja esta call é o conteúdo que ela própria consome por handle, e não outra coisa
// que o modelo já viu.
//
// É ESTA LINHA QUE COMPRA A FASE R2. Sem ela, uma call que consome um handle untrusted continua
// negada pela regra binária, a separação de planos não muda nenhuma decisão, e o motivo de existir
// da fase não se realiza.
//
// E É ESTA LINHA QUE PRESERVA O CONTROLO NEGATIVO. Num run com media projectada para o modelo o
// piso já é untrusted, logo a proveniência é `context` e não `handle_argument`: a disjunção falha
// nos dois lados e a call é negada. A política não sabe o que é media — decide pela etiqueta.
//
// O SA-ROC do ADR-013 NÃO MUDA: enviar continua a ser enviar, a sensibilidade efectiva continua a
// ser classificada, e o egress continua default-deny com allowlist. Esta regra decide autorização,
// não classificação.
@id("allow_fs_read_external")
@flow_from_handle("external_read")
permit (
    principal,
    action == Action::"cap:fs.read",
    resource
)
when {
    principal.authority.contains("cap:fs.read") &&
    context.destination == "external_read" &&
    (context.taint != "untrusted" || context.provenance == "handle_argument")
};

// allow_artifact_write — INALTERADA face à 1.1.0 (AOS-549). Gravar no armazém de artefactos do
// PRÓPRIO run é escrita local do run e é permitida também com o contexto untrusted; a anotação
// `@taint_exempt` diz porquê, e o gate `policy-taint` só a aceita para as capabilities de escrita
// local do run que ele conhece.
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
