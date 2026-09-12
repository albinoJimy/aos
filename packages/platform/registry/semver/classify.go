package semver

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// egressRank ordena as classes de egress por RISCO crescente. Uma elevação de
// classe (rank maior) é uma quebra de contrato (MAJOR); uma redução é uma
// alteração retro-compatível (MINOR). Uma classe desconhecida recebe o rank
// máximo+1 para que qualquer mudança de/para ela seja tratada conservadoramente
// como elevação (fail-closed).
func egressRank(e domain.EgressClass) int {
	switch e {
	case domain.EgressNone:
		return 0
	case domain.EgressInternal:
		return 1
	case domain.EgressExternal:
		return 2
	default:
		return 3
	}
}

// ChangeRequest descreve uma transição PROPOSTA de contrato — o contrato antigo e o
// novo, as versões correspondentes e um sinal explícito de quebra semântica. É a
// entrada da validação de bump.
type ChangeRequest struct {
	// Kind é o tipo de artefacto (apenas para atributos de span; não afecta a decisão).
	Kind domain.ArtifactKind
	// From é a versão da qual se parte (a versão active corrente do artefacto).
	From domain.Version
	// To é a versão proposta para a nova entrada.
	To domain.Version
	// OldContract é o contrato público da versão From.
	OldContract domain.Contract
	// NewContract é o contrato público proposto para a versão To.
	NewContract domain.Contract
	// SemanticsBroken permite ao publicador DECLARAR uma quebra de comportamento
	// mesmo quando o schema/scopes/egress são byte-idênticos (a semântica quebrada
	// não é derivável do contrato estrutural). Quando true, a mudança exigida é
	// sempre MAJOR. É o único sinal não-estrutural; tudo o resto é deduzido.
	SemanticsBroken bool
}

// Classification é o veredicto determinista da validação: a mudança MÍNIMA que o
// contrato exige (Required), a mudança DECLARADA pelo delta de versão (Declared) e
// os factores que contribuíram para Required (Reasons, ordenados de forma estável
// para diagnóstico e igualdade em testes).
type Classification struct {
	Required domain.ChangeKind
	Declared domain.ChangeKind
	Reasons  []string
}

// ClassifyContract deduz a mudança MÍNIMA de bump que a transição de old para new
// exige, ancorada ao CONTRATO PÚBLICO (schema de I/O + scopes de credencial +
// classe de egress) mais o sinal de quebra semântica. Determinista e pura.
//
//   - MAJOR (quebra): schema de INPUT com propriedade removida/tornada
//     obrigatória/tipo incompatível/constrangida; schema de OUTPUT com propriedade
//     removida/garantia enfraquecida; scopes de credencial ACRESCENTADOS; classe de
//     egress ELEVADA; semântica declarada quebrada. É a única classe que justifica
//     re-aprovação e novo estado de confiança TOFU.
//
// NOTA DE ESCOPO (AOS-052): a relação "MAJOR ⇒ nova confiança TOFU" é SEMÂNTICA/
// documental. tofu.Monitor.Reapprove (AOS-049) NÃO consome ClassifyContract — exige
// apenas uma versão SemVer estritamente superior — e permanece intacto por decisão
// de escopo. Ligar a re-aprovação TOFU à classificação MAJOR (enforcement, não só
// semântica) é trabalho futuro fora de AOS-052.
//   - MINOR (retro-compatível): adição de campo opcional, relaxamento de
//     obrigatoriedade, scopes removidos, egress reduzido, ou qualquer mudança de
//     contrato sem factor de quebra.
//   - PATCH/None: contrato byte-idêntico (mesmo schema/scopes/egress).
//
// Devolve a mudança exigida e a lista ordenada de razões contribuintes.
func ClassifyContract(old, new domain.Contract, semanticsBroken bool) (domain.ChangeKind, []string) {
	required := domain.ChangeNone
	reasons := map[string]struct{}{}
	bump := func(k domain.ChangeKind, reason string) {
		if k > required {
			required = k
		}
		if k != domain.ChangeNone {
			reasons[reason] = struct{}{}
		}
	}

	// Egress: elevação = quebra; redução = retro-compatível.
	if or, nr := egressRank(old.Egress), egressRank(new.Egress); or != nr {
		if nr > or {
			bump(domain.ChangeMajor, "egress_elevated")
		} else {
			bump(domain.ChangeMinor, "egress_reduced")
		}
	}

	// Credential scopes: acrescentar = quebra (mais credenciais pedidas → re-aprovação);
	// remover = retro-compatível.
	added, removed := diffScopes(old.CredentialScopes, new.CredentialScopes)
	if added {
		bump(domain.ChangeMajor, "scopes_added")
	}
	if removed {
		bump(domain.ChangeMinor, "scopes_removed")
	}

	// Schemas de I/O: diferença estrutural com direcção de compatibilidade por papel.
	ik, ir := classifySchema(roleInput, old.InputSchema, new.InputSchema)
	bump(ik, ir)
	ok, or2 := classifySchema(roleOutput, old.OutputSchema, new.OutputSchema)
	bump(ok, or2)

	// Manifesto do servidor MCP: qualquer delta é MAJOR (AOS-335).
	//
	// PORQUÊ MAJOR, E NÃO UMA CLASSE MAIS FINA. O `ManifestDigest` é uma ÂNCORA OPACA — o
	// `mcp.DigestAncorado` funde a superfície declarada do servidor com o endpoint onde ele
	// atende, e o resultado não é decomponível a jusante. Daqui não se consegue distinguir «o
	// servidor mudou de porta» de «o servidor ganhou uma tool `exec`». Perante duas leituras
	// possíveis e nenhuma forma de as separar, a classificação tem de ser a da pior.
	//
	// E é a leitura certa para o que o campo significa: o digest do manifesto é o que diz que
	// o servidor por trás deste contrato é o MESMO que foi aprovado. Mudou ⇒ não é. Chamar
	// PATCH a uma troca de identidade seria o fail-open que o AOS-320 fechou no digest,
	// reaberto na classificação — que é literalmente o defeito que este ticket corrige: a
	// omissão não deixava a classificação indefinida, DEGRADAVA-A para «compatível».
	//
	// CUSTO DECLARADO, contra o ADR-012: uma mudança PURAMENTE OPERACIONAL de endpoint, sem
	// alteração nenhuma de superfície, passa a exigir bump MAJOR. É caro e é deliberado —
	// separar os dois eixos exige decompor a âncora, que é trabalho no `mcp.DigestAncorado` e
	// não aqui. Fica nomeado em vez de resolvido por uma heurística que adivinhasse qual dos
	// dois mudou.
	//
	// SÓ DISPARA COM DELTA REAL: as tools e skills genéricas têm o campo vazio dos dois lados,
	// pelo que `old == new` e nada acontece.
	//
	// QUEM SE MOVE, ALÉM DOS `mcp_server`: as entradas `kind=tool` DERIVADAS de um servidor
	// MCP transportam a mesma âncora (`mcp.Host` grava-a em cada tool que expõe), pelo que
	// mover o endpoint de um servidor com N tools exige MAJOR nas N. É a maior parte do custo
	// real desta regra, e a primeira redacção deste comentário omitia-a.
	if old.ManifestDigest != new.ManifestDigest {
		bump(domain.ChangeMajor, "manifest_digest_changed")
	}

	// Semântica declarada quebrada: MAJOR mesmo sem mudança estrutural.
	if semanticsBroken {
		bump(domain.ChangeMajor, "semantics_broken")
	}

	return required, sortedReasons(reasons)
}

// schemaRole distingue a direcção de compatibilidade: o schema de INPUT é
// consumido pelos CHAMADORES (tornar mais exigente quebra-os); o de OUTPUT é
// produzido pelo artefacto e consumido a jusante (retirar/enfraquecer garantias
// quebra os consumidores).
type schemaRole int

const (
	roleInput schemaRole = iota
	roleOutput
)

// classifySchema deduz a mudança que a evolução de um schema (input ou output)
// exige. Compara os dois schemas de forma ESTRUTURAL e RECURSIVA (não só o nível de
// topo), apanhando quebras ANINHADAS (uma propriedade dentro de outra) e por
// KEYWORD DE CONSTRANGIMENTO (enum, minLength/maximum/…, pattern, format,
// additionalProperties, items) que uma análise só-de-topo deixaria passar como MINOR
// (AOS-052-Q1). Fail-closed e COERENTE:
//
//   - um schema que não seja um objecto decodificável e que MUDE é quebra (MAJOR);
//   - AMBOS os schemas malformados mas com bytes DIFERENTES são uma quebra OPACA
//     (MAJOR), nunca ChangeNone — dois documentos malformados distintos são uma
//     mudança de contrato invisível se colapsados a "sem sinal" (AOS-052-Q4);
//   - o APERTO de qualquer constrangimento no papel relevante (INPUT a ficar mais
//     exigente; OUTPUT a enfraquecer uma garantia) é quebra; um constrangimento cujo
//     VALOR muda de forma não-provada-compatível é tratado conservadoramente como
//     quebra.
func classifySchema(role schemaRole, oldRaw, newRaw json.RawMessage) (domain.ChangeKind, string) {
	oc, oerr := digest.CanonicalJSON(oldRaw)
	nc, nerr := digest.CanonicalJSON(newRaw)
	// JSON mal-formado num dos lados → tratado como quebra opaca (fail-closed).
	if oerr != nil || nerr != nil {
		if oerr != nil && nerr != nil {
			// Ambos inválidos: só é "sem sinal" se forem BYTE-IDÊNTICOS; dois
			// documentos malformados DISTINTOS são uma mudança opaca (AOS-052-Q4).
			if bytes.Equal(oldRaw, newRaw) {
				return domain.ChangeNone, ""
			}
			return domain.ChangeMajor, schemaReason(role, "opaque_change")
		}
		return domain.ChangeMajor, schemaReason(role, "opaque_change")
	}
	if bytes.Equal(oc, nc) {
		return domain.ChangeNone, ""
	}

	breaking, ok := schemaBreaking(role, oc, nc)
	if !ok {
		// Mudou mas não é um object-schema analisável (array/escalar) → conservador.
		return domain.ChangeMajor, schemaReason(role, "opaque_change")
	}
	if breaking {
		return domain.ChangeMajor, schemaReason(role, "breaking")
	}
	// Mudou de forma não-quebrada (campo opcional novo, relaxamento, metadados):
	// retro-compatível → MINOR (o contrato público mudou, logo nunca PATCH).
	return domain.ChangeMinor, schemaReason(role, "compatible")
}

// constraintKeywords é o conjunto de keywords de CONSTRANGIMENTO JSON-Schema que
// apertam/enfraquecem o schema (para além de type/properties/required, tratados
// estruturalmente). A mera PRESENÇA de qualquer uma restringe o schema; a sua
// remoção relaxa-o. "additionalProperties" é a excepção: a forma restritiva é o
// valor `false` (a ausência ~ `true`, permissivo). Keywords fora desta lista (ex.:
// description, title, examples, default) são metadados e nunca são quebra.
var constraintKeywords = []string{
	"enum", "const",
	"minLength", "maxLength", "pattern", "format",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minItems", "maxItems", "uniqueItems", "items",
	"minProperties", "maxProperties", "additionalProperties",
}

// schemaBreaking compara dois schemas canonicalizados como OBJECTOS e devolve se a
// mudança quebra o papel dado. ok=false se algum lado não for um objecto JSON
// analisável (o chamador trata como opaco/conservador). Presume oc != nc.
func schemaBreaking(role schemaRole, oc, nc []byte) (breaking, ok bool) {
	om := decodeObject(oc)
	nm := decodeObject(nc)
	if om == nil || nm == nil {
		return false, false
	}
	return schemaObjectsBreaking(role, om, nm), true
}

// schemaObjectsBreaking aplica as regras de quebra por keyword sobre dois objectos
// de schema já decodificados.
func schemaObjectsBreaking(role schemaRole, om, nm map[string]json.RawMessage) bool {
	breaking := false
	if typeKeywordBreaking(role, om["type"], nm["type"]) {
		breaking = true
	}
	if propertiesBreaking(role, om["properties"], nm["properties"]) {
		breaking = true
	}
	if requiredBreaking(role, om["required"], nm["required"]) {
		breaking = true
	}
	for _, k := range constraintKeywords {
		if constraintBreaking(role, k, om[k], nm[k]) {
			breaking = true
		}
	}
	return breaking
}

// typeKeywordBreaking decide a quebra do keyword "type". Tipos simples (string ou
// ausente) usam typeIncompatible; um tipo COMPOSTO (lista) de qualquer lado é
// comparado canonicamente e qualquer diferença é conservadoramente quebra.
func typeKeywordBreaking(role schemaRole, oldRaw, newRaw json.RawMessage) bool {
	oStr, oSimple := simpleType(oldRaw)
	nStr, nSimple := simpleType(newRaw)
	if oSimple && nSimple {
		return typeIncompatible(role, oStr, nStr)
	}
	return !rawSchemaEqual(oldRaw, newRaw)
}

// simpleType devolve o valor de "type" como string. ok=true quando ausente (string
// vazia) ou uma string simples; ok=false para um tipo composto (lista de tipos).
func simpleType(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var t string
	if err := json.Unmarshal(raw, &t); err == nil {
		return t, true
	}
	return "", false
}

// propertiesBreaking compara os sub-schemas de "properties" recursivamente. Uma
// propriedade REMOVIDA é quebra para ambos os papéis (consistente com a semântica
// original do REG); uma propriedade presente em ambos recorre; uma propriedade
// ACRESCENTADA é compatível (a obrigatoriedade é tratada em requiredBreaking).
func propertiesBreaking(role schemaRole, oldRaw, newRaw json.RawMessage) bool {
	op := decodeObject(oldRaw)
	np := decodeObject(newRaw)
	breaking := false
	for name, oldSub := range op {
		newSub, ok := np[name]
		if !ok {
			breaking = true
			continue
		}
		if subschemaBreaking(role, oldSub, newSub) {
			breaking = true
		}
	}
	return breaking
}

// requiredBreaking aplica a direcção de compatibilidade da obrigatoriedade: no
// INPUT tornar um campo obrigatório quebra os chamadores; no OUTPUT deixar de
// garantir um campo antes garantido quebra a jusante.
func requiredBreaking(role schemaRole, oldRaw, newRaw json.RawMessage) bool {
	oldReq := decodeStringSet(oldRaw)
	newReq := decodeStringSet(newRaw)
	if role == roleInput {
		for name := range newReq {
			if !oldReq[name] {
				return true // passou a obrigatória → quebra o input
			}
		}
		return false
	}
	for name := range oldReq {
		if !newReq[name] {
			return true // deixou de ser garantida → quebra o output
		}
	}
	return false
}

// constraintBreaking decide a quebra de UMA keyword de constrangimento. No INPUT,
// um schema MAIS restritivo (acrescentar/alterar uma restrição) rejeita entradas
// antes aceites → quebra. No OUTPUT, um schema MENOS restritivo (remover/alterar
// uma garantia) enfraquece o contrato → quebra. Uma alteração de VALOR entre dois
// constrangimentos activos é tratada conservadoramente como quebra (fail-closed).
func constraintBreaking(role schemaRole, keyword string, oldRaw, newRaw json.RawMessage) bool {
	oldR := restrictiveActive(keyword, oldRaw)
	newR := restrictiveActive(keyword, newRaw)
	switch role {
	case roleInput:
		if !oldR && newR {
			return true // acrescentou uma restrição → aperta o input
		}
		if oldR && newR && !rawSchemaEqual(oldRaw, newRaw) {
			return true // restrição alterada → conservador (pode apertar)
		}
		return false
	default: // roleOutput
		if oldR && !newR {
			return true // removeu uma garantia → enfraquece o output
		}
		if oldR && newR && !rawSchemaEqual(oldRaw, newRaw) {
			return true // garantia alterada → conservador (pode enfraquecer)
		}
		return false
	}
}

// restrictiveActive indica se a keyword está presente na sua forma RESTRITIVA. Para
// a generalidade das keywords, a mera presença restringe. "additionalProperties" é
// restritivo apenas quando o valor é `false` (a ausência ~ `true`, permissivo); como
// sub-schema (objecto) é uma restrição de forma → restritivo.
func restrictiveActive(keyword string, raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if keyword == "additionalProperties" {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return !b // additionalProperties:false é a forma restritiva
		}
		return true
	}
	return true
}

// subschemaBreaking compara dois sub-schemas (valores de uma propriedade/items)
// recursivamente. Sub-schemas byte-idênticos após canonicalização não mudam; um
// sub-schema não-objecto (booleano/escalar) que difira é conservadoramente quebra.
func subschemaBreaking(role schemaRole, oldRaw, newRaw json.RawMessage) bool {
	oc, oerr := digest.CanonicalJSON(oldRaw)
	nc, nerr := digest.CanonicalJSON(newRaw)
	if oerr != nil || nerr != nil {
		// Não canonicalizável: comparar bytes crus; diferença → conservador (quebra).
		return !bytes.Equal(oldRaw, newRaw)
	}
	if bytes.Equal(oc, nc) {
		return false
	}
	breaking, ok := schemaBreaking(role, oc, nc)
	if !ok {
		return true // sub-schema não-objecto que difere → conservador
	}
	return breaking
}

// decodeObject descodifica raw como um objecto JSON. Devolve nil se ausente/vazio
// ou se não for um objecto (o chamador trata a ausência conservadoramente).
func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// decodeStringSet descodifica raw como um conjunto de strings (array "required").
// Um valor ausente ou não-array devolve um conjunto vazio.
func decodeStringSet(raw json.RawMessage) map[string]bool {
	out := map[string]bool{}
	if len(raw) == 0 {
		return out
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return out
	}
	for _, s := range arr {
		out[s] = true
	}
	return out
}

// rawSchemaEqual compara dois fragmentos JSON pela sua forma canónica (ordem de
// chaves/whitespace irrelevantes); recai em igualdade de bytes crus se algum não
// for canonicalizável.
func rawSchemaEqual(a, b json.RawMessage) bool {
	ac, aerr := digest.CanonicalJSON(a)
	bc, berr := digest.CanonicalJSON(b)
	if aerr != nil || berr != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ac, bc)
}

// typeIncompatible decide se a mudança do keyword "type" de um campo é
// incompatível para o papel dado. Vazio significa "sem constrangimento de tipo".
//   - INPUT: adicionar/apertar um tipo (vazio→concreto, ou concreto→concreto
//     diferente) rejeita entradas antes aceites → quebra; remover o tipo
//     (concreto→vazio) relaxa → não quebra.
//   - OUTPUT: remover/mudar um tipo antes garantido enfraquece a garantia → quebra;
//     adicionar um tipo (vazio→concreto) reforça → não quebra.
func typeIncompatible(role schemaRole, oldTyp, newTyp string) bool {
	if oldTyp == newTyp {
		return false
	}
	switch role {
	case roleInput:
		// Relaxar (remover o constrangimento) é compatível; tudo o resto aperta.
		return newTyp != ""
	default: // roleOutput
		// Reforçar (adicionar um constrangimento onde não havia) é compatível.
		return oldTyp != ""
	}
}

func schemaReason(role schemaRole, kind string) string {
	prefix := "input_schema_"
	if role == roleOutput {
		prefix = "output_schema_"
	}
	return prefix + kind
}

// diffScopes indica se, do conjunto old para o new, foram ACRESCENTADOS e/ou
// REMOVIDOS scopes de credencial (comparação por conjunto; duplicados e ordem são
// irrelevantes).
func diffScopes(old, new []string) (added, removed bool) {
	oldSet := make(map[string]struct{}, len(old))
	for _, s := range old {
		oldSet[s] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(new))
	for _, s := range new {
		newSet[s] = struct{}{}
	}
	for s := range newSet {
		if _, ok := oldSet[s]; !ok {
			added = true
		}
	}
	for s := range oldSet {
		if _, ok := newSet[s]; !ok {
			removed = true
		}
	}
	return added, removed
}

func sortedReasons(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// ValidateBump é o GATE de versionamento (fail-closed). Classifica a mudança de
// contrato exigida e VALIDA que o bump declarado pelo delta de versão a satisfaz:
//
//   - To tem de ser ESTRITAMENTE superior a From (um bump é um avanço) →
//     ErrNonMonotonicBump caso contrário.
//   - O bump declarado (delta de versão) tem de ser >= à mudança exigida pelo
//     contrato → ErrIncompatibleBump caso contrário. Isto REJEITA em particular uma
//     quebra de contrato publicada como MINOR/PATCH.
//
// A sobre-declaração (declarar MAJOR para uma mudança apenas MINOR) é PERMITIDA: um
// publicador pode ser conservador. A classificação é a fonte de verdade do MÍNIMO.
//
// NOTA DE ESCOPO (AOS-052): este gate é uma BIBLIOTECA autónoma. registry.Publish/
// SetStatus NÃO o invocam ainda — a integração de ValidateBump na fronteira de
// publicação/promoção (resolvendo o contrato From da versão active para alimentar o
// gate) é responsabilidade explícita de AOS-053. Até lá a semântica SemVer só é
// imposta quando o chamador invoca ValidateBump/Gate.Validate directamente.
func ValidateBump(req ChangeRequest) (Classification, error) {
	required, reasons := ClassifyContract(req.OldContract, req.NewContract, req.SemanticsBroken)
	declared := domain.Classify(req.From, req.To)
	cl := Classification{Required: required, Declared: declared, Reasons: reasons}

	if req.To.Compare(req.From) <= 0 {
		return cl, ErrNonMonotonicBump
	}
	if declared < required {
		return cl, ErrIncompatibleBump
	}
	return cl, nil
}

// Atributos de span do gate de versionamento (públicos: versões e níveis de
// mudança não são segredos). Reutilizam a porta Tracer zero-dep do Agent Runtime.
const (
	attrArtifactID   = agentruntime.AttrToolName
	attrFromVersion  = "aos.registry.from_version"
	attrToVersion    = "aos.registry.to_version"
	attrRequiredKind = "aos.registry.required_change"
	attrDeclaredKind = "aos.registry.declared_change"
	attrDecision     = "aos.registry.decision"
	attrReason       = "aos.registry.reason"
)

const opValidateBump = "registry.semver.validate_bump"

// Gate é o ponto de decisão OBSERVÁVEL da validação de bump: envolve ValidateBump
// num span OTel (via a porta Tracer zero-dep), sem alterar a semântica. A decisão
// permanece determinista e pura — o tracer é apenas observação. Construir com
// [NewGate].
type Gate struct {
	tracer agentruntime.Tracer
}

// Option configura o Gate.
type Option func(*Gate)

// WithTracer injecta a porta de observabilidade. Por omissão NoopTracer. Nenhum
// segredo entra num span (só versões e níveis de mudança).
func WithTracer(t agentruntime.Tracer) Option {
	return func(g *Gate) {
		if t != nil {
			g.tracer = t
		}
	}
}

// NewGate constrói o gate de versionamento.
func NewGate(opts ...Option) *Gate {
	g := &Gate{tracer: agentruntime.NoopTracer{}}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Validate é ValidateBump instrumentado com um span. O veredicto é idêntico ao da
// função pura; o span leva as versões, os níveis de mudança e a decisão.
func (g *Gate) Validate(ctx context.Context, req ChangeRequest) (Classification, error) {
	_, span := g.tracer.StartSpan(ctx, opValidateBump)
	defer span.End()
	span.SetAttribute(attrArtifactID, string(req.Kind))
	span.SetAttribute(attrFromVersion, req.From.String())
	span.SetAttribute(attrToVersion, req.To.String())

	cl, err := ValidateBump(req)
	span.SetAttribute(attrRequiredKind, cl.Required.String())
	span.SetAttribute(attrDeclaredKind, cl.Declared.String())
	if len(cl.Reasons) > 0 {
		span.SetAttribute(attrReason, cl.Reasons)
	}
	if err != nil {
		span.SetAttribute(attrDecision, "rejected")
		return cl, err
	}
	span.SetAttribute(attrDecision, "accepted")
	return cl, nil
}
