package pdp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
)

// smokeActionHeadRe extrai a capability do scope de acção de uma regra (`action ==
// Action::"cap:..."`) a partir da sua forma canónica ([cedar.Policy.MarshalCedar]). Serve a
// AVALIAÇÃO DE FUMO DINÂMICA (AOS-378): a sonda apresenta um request cuja acção CASE o scope
// da regra, para o motor avaliar o `when`. É um COMPLEMENTO da verificação estática abaixo, não
// a garantia: a sonda dinâmica é intrinsecamente incompleta — não alcança regras com `action in
// [...]` (scope de acção múltipla) e curto-circuita quando o `when` exige um valor num atributo
// mapeado que a semente não satisfaz. Por isso a detecção de atributo-fora-do-mapa NÃO depende
// dela; depende de [attrRefsForaDoMapa], que é estática e imune a ambos.
var smokeActionHeadRe = regexp.MustCompile(`action\s*==\s*Action::"([^"]+)"`)

// smokeAttrMapaFixo é o mapa de atributos que [cedarEngine.evaluate] monta em runtime
// (engine_cedar.go: principal.authority, resource.region, context.taint, context.sensitivity) e
// SÓ ele. Uma regra que refira qualquer outro atributo compila e assina, mas em runtime levanta
// `diag.Errors` ⇒ ErrMalformedRequest ⇒ deny — e, como esse erro contamina TODA a autorização da
// capability (evaluate falha antes de decidir), um único permit mal-atributado faz deny-all
// silencioso. É o defeito que a alínea (c) do AOS-378 fecha.
var smokeAttrMapaFixo = map[string]bool{
	"principal.authority": true,
	"resource.region":     true,
	"context.taint":       true,
	"context.sensitivity": true,
}

// smokeRaizes são as raízes de request cujos atributos o motor mapeia. `action` NÃO entra: é
// referida só no scope (`action == Action::"..."` / `action in [...]`), nunca por atributo.
var smokeRaizes = []string{"principal", "resource", "context"}

// smokeAttrSet são os atributos mapeados de tipo Set — HOJE só `principal.authority`. Só sobre
// ESTES um método de set (`.contains`/`.containsAny`/`.containsAll`) é legítimo. Os outros três
// atributos mapeados são String: um método de set sobre uma String COMPILA mas erra em runtime
// («expected set, got string») ⇒ ErrMalformedRequest ⇒ deny-all — logo tem de ser sinalizado.
var smokeAttrSet = map[string]bool{
	"principal.authority": true,
}

func smokeEhIdentChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func smokeEhEspaco(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// attrRefsForaDoMapa devolve, por ordem determinista e sem repetições, os acessos a atributos de
// `principal`/`resource`/`context` na regra `marshaled` que NÃO existem em runtime — i.e. fora de
// [smokeAttrMapaFixo].
//
// É ANÁLISE ESTÁTICA sobre a forma canónica ([cedar.Policy.MarshalCedar]) — não uma avaliação. É
// esta função (não a sonda dinâmica) que cumpre o requisito da alínea (c): apanha o atributo mau
// INDEPENDENTEMENTE da alcançabilidade, imune ao curto-circuito do `&&`, à forma `action in [...]`
// e à forma de índice `raiz["chave"]` que a sonda dinâmica ou uma regex ingénua deixavam escapar.
//
// É um SCANNER de caracteres, não uma regex, porque a distinção que interessa não é regular:
//   - `context.reversibility`         ⇒ FORA (o record não tem a chave)                → apanhado
//   - `context["foo-bar"]`            ⇒ FORA (índice de chave não-mapeada)             → apanhado
//   - `context.taint.foo`             ⇒ FORA (acesso ENCADEADO sobre um escalar/set)   → apanhado
//   - `principal.authority.contains(` ⇒ OK   (chamada de MÉTODO de set, não acesso)    → ignorado
//   - `context has reversibility`     ⇒ OK   (o `has` devolve bool, não erra em runtime)→ ignorado
//   - `context.sensitivity == "x"`    ⇒ OK   (atributo mapeado)                        → ignorado
//
// Literais de string são saltados durante a varredura, para que um `@id("...context.x...")` ou uma
// string de dados não conte como acesso. A distinção método-vs-acesso encadeado (o `(` a seguir) é
// o que uma regex não capta e o que gerou os falsos negativos da primeira tentativa.
func attrRefsForaDoMapa(marshaled []byte) []string {
	s := marshaled
	n := len(s)
	vistos := make(map[string]bool)
	var fora []string
	add := func(ref string) {
		if !vistos[ref] {
			vistos[ref] = true
			fora = append(fora, ref)
		}
	}
	saltarEspacos := func(j int) int {
		for j < n && smokeEhEspaco(s[j]) {
			j++
		}
		return j
	}
	i := 0
	for i < n {
		// Saltar literais de string (com escapes) — não são acessos a atributos.
		if s[i] == '"' {
			i++
			for i < n {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		// Tentar casar uma raiz num limite de palavra.
		raiz := ""
		for _, r := range smokeRaizes {
			if i+len(r) <= n && string(s[i:i+len(r)]) == r {
				if i > 0 && smokeEhIdentChar(s[i-1]) {
					continue // sufixo de outro identificador (ex.: `mycontext`)
				}
				if j := i + len(r); j < n && smokeEhIdentChar(s[j]) {
					continue // prefixo de outro identificador (ex.: `contextual`)
				}
				raiz = r
				break
			}
		}
		if raiz == "" {
			i++
			continue
		}
		j := saltarEspacos(i + len(raiz))
		switch {
		case j < n && s[j] == '.':
			// raiz.attr1
			j = saltarEspacos(j + 1)
			ini := j
			for j < n && smokeEhIdentChar(s[j]) {
				j++
			}
			attr1 := string(s[ini:j])
			if attr1 == "" {
				i = j
				continue
			}
			ref := raiz + "." + attr1
			if !smokeAttrMapaFixo[ref] {
				add(ref) // atributo de topo fora do mapa
				i = j
				continue
			}
			// Atributo mapeado: o que se segue decide.
			//   - `.ident(` é MÉTODO — legítimo SÓ se `ref` for um Set ([smokeAttrSet], hoje só
			//     principal.authority). Um método de set sobre uma String (region/taint/sensitivity)
			//     compila mas erra em runtime («expected set, got string») ⇒ sinalizar.
			//   - `.ident` sem `(` é acesso ENCADEADO sobre um escalar/set (erro) ⇒ sinalizar.
			//   - `[` é índice sobre um escalar/set (erro) ⇒ sinalizar.
			k := saltarEspacos(j)
			if k < n && s[k] == '.' {
				k = saltarEspacos(k + 1)
				ns := k
				for k < n && smokeEhIdentChar(s[k]) {
					k++
				}
				ident2 := string(s[ns:k])
				m := saltarEspacos(k)
				ehMetodo := m < n && s[m] == '('
				if ident2 != "" && !(ehMetodo && smokeAttrSet[ref]) {
					// acesso encadeado, OU método de set sobre um atributo que não é Set
					add(ref + "." + ident2)
				}
			} else if k < n && s[k] == '[' {
				add(ref + "[...]")
			}
			i = j
		case j < n && s[j] == '[':
			// raiz["chave"] — forma de índice. Chave mapeada (identificador) já viria em ponto;
			// o que sobrevive em brackets é sempre chave fora do mapa. Confirma-se mesmo assim.
			k := saltarEspacos(j + 1)
			if k < n && s[k] == '"' {
				k++
				ks := k
				for k < n {
					if s[k] == '\\' {
						k += 2
						continue
					}
					if s[k] == '"' {
						break
					}
					k++
				}
				chave := string(s[ks:k])
				if !smokeAttrMapaFixo[raiz+"."+chave] {
					add(raiz + `["` + chave + `"]`)
				}
				// AVANÇAR PARA ALÉM da aspa de fecho (k aponta-lhe): `i = k` deixaria a próxima
				// iteração a ver essa aspa como INÍCIO de string e a saltar conteúdo real até à
				// aspa seguinte — dessincronizando a paridade de literais no resto da varredura.
				if k < n {
					k++
				}
				i = k
			} else {
				add(raiz + "[...]") // índice não-string (conservador)
				i = j + 1
			}
		default:
			// raiz usada como referência de entidade (`principal in ...`, `context has x`) — OK.
			i = j
		}
	}
	sort.Strings(fora)
	return fora
}

// Nomes de tipo de entidade Cedar usados no mapeamento Input → request.
const (
	entityPrincipal = cedar.EntityType("Principal")
	entityResource  = cedar.EntityType("Resource")
	entityAction    = cedar.EntityType("Action")
)

// cedarEngine encapsula a policy set Cedar COMPILADA em memória (uma vez, no
// load) e a versão de política associada. É imutável após construção, logo
// seguro para avaliação concorrente e pura.
type cedarEngine struct {
	policies *cedar.PolicySet
	version  string
	// allow é a allowlist de capabilities (AOS-007) compilada a partir do MESMO
	// bundle assinado. Trocada atomicamente com as regras Cedar em hot-reload,
	// nunca divergindo da versão de política em vigor.
	allow *Allowlist
}

// compilePolicies compila os ficheiros .cedar do bundle numa única policy set.
// Cada política é registada com o id da sua anotação @id (ex. "allow_http_post")
// para que a razão da decisão nomeie a regra; na ausência de @id usa
// "<ficheiro>#<índice>". Um id duplicado é erro (ambiguidade de regra).
func compilePolicies(files map[string][]byte) (*cedar.PolicySet, error) {
	ps := cedar.NewPolicySet()

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		// Só os *.cedar são regras Cedar; os `capabilities/*.json` (allowlist do
		// AOS-007) partilham o mapa (para entrarem no hash/assinatura) mas NÃO
		// compilam como política Cedar.
		if !strings.HasSuffix(n, cedarExt) {
			continue
		}
		list, err := cedar.NewPolicyListFromBytes(n, files[n])
		if err != nil {
			return nil, fmt.Errorf("compilar politica %q: %w", n, err)
		}
		for i, pol := range list {
			id := annotationID(pol)
			if id == "" {
				id = fmt.Sprintf("%s#%d", n, i)
			}
			if !ps.Add(cedar.PolicyID(id), pol) {
				return nil, fmt.Errorf("politica com id duplicado: %q", id)
			}
		}
	}
	return ps, nil
}

// ruleIDs devolve, ORDENADOS, os identificadores (@id) das regras Cedar
// COMPILADAS na policy set — o mesmo id por que a razão de uma decisão nomeia a
// regra (ver [compilePolicies]/[annotationID]). É estritamente SÓ-LEITURA: itera
// a policy set imutável e não toca no caminho de avaliação, pelo que expô-lo NÃO
// altera nenhuma decisão. Suporta a verificação de cobertura por-regra (AOS-113):
// enumerar as regras reais permite FALHAR se alguma ficar sem casos allow+deny.
func (e *cedarEngine) ruleIDs() []string {
	out := make([]string, 0)
	// All() é o iterador não-depreciado (v1.8) sobre (PolicyID, *Policy); usamos só
	// a chave. A ordem de iteração de um mapa não é determinista — ordenamos.
	for id := range e.policies.All() {
		out = append(out, string(id))
	}
	sort.Strings(out)
	return out
}

// annotationID devolve o valor da anotação @id de uma política, ou "" se ausente.
func annotationID(p *cedar.Policy) string {
	for k, v := range p.Annotations() {
		if string(k) == "id" {
			return string(v)
		}
	}
	return ""
}

// newCedarEngine compila os ficheiros e devolve um motor pronto a avaliar.
func newCedarEngine(files map[string][]byte, version string) (*cedarEngine, error) {
	ps, err := compilePolicies(files)
	if err != nil {
		return nil, err
	}
	al, err := parseAllowlist(files)
	if err != nil {
		return nil, err
	}
	return &cedarEngine{policies: ps, version: version, allow: al}, nil
}

// evaluate mapeia o [Input] para um request Cedar e autoriza-o contra a policy
// set. Devolve (allow, reason). É pura e determinística: a mesma (Input,
// policies) produz sempre o mesmo resultado. Um erro de avaliação Cedar (ex.:
// atributo em falta) é fail-closed → deny com erro.
func (e *cedarEngine) evaluate(in Input) (allow bool, reason string, err error) {
	principalUID := cedar.NewEntityUID(entityPrincipal, cedar.String(orDefault(in.Principal.ID, "anonymous")))
	resourceUID := cedar.NewEntityUID(entityResource, cedar.String(orDefault(in.Resource.Value, "resource")))
	actionUID := cedar.NewEntityUID(entityAction, cedar.String(in.Capability))

	authVals := make([]cedar.Value, 0, len(in.Principal.Authority))
	for _, a := range in.Principal.Authority {
		authVals = append(authVals, cedar.String(a))
	}

	entities := cedar.EntityMap{
		principalUID: cedar.Entity{
			UID: principalUID,
			Attributes: cedar.NewRecord(cedar.RecordMap{
				"authority": cedar.NewSet(authVals...),
			}),
		},
		resourceUID: cedar.Entity{
			UID: resourceUID,
			Attributes: cedar.NewRecord(cedar.RecordMap{
				"region": cedar.String(in.Resource.Region),
			}),
		},
	}

	req := cedar.Request{
		Principal: principalUID,
		Action:    actionUID,
		Resource:  resourceUID,
		Context: cedar.NewRecord(cedar.RecordMap{
			"taint":       cedar.String(in.Context.Taint),
			"sensitivity": cedar.String(in.Context.Sensitivity),
		}),
	}

	decision, diag := cedar.Authorize(e.policies, entities, req)
	if len(diag.Errors) > 0 {
		return false, fmt.Sprintf("erro de avaliacao de politica para %q", in.Capability),
			fmt.Errorf("%w: cedar: %s", ErrMalformedRequest, diag.Errors[0].Message)
	}
	if decision == cedar.Allow {
		rule := ""
		if len(diag.Reasons) > 0 {
			rule = string(diag.Reasons[0].PolicyID)
		}
		return true, fmt.Sprintf("capability %s permitida por regra %s", in.Capability, rule), nil
	}
	// `camada=cedar` NOMEIA a camada que produziu esta negação (AOS-378): esta é a
	// recusa do MOTOR Cedar (nenhum permit aplicável), distinta da recusa da ALLOWLIST
	// (capabilities.go, `camada=allowlist`) que corre ANTES e produz o MESMO substring
	// "default-deny". O token estável permite ao assert de cobertura por-regra exigir que
	// um deny tenha mesmo ALCANÇADO o Cedar, em vez de contar uma recusa da allowlist como
	// cobertura de uma regra Cedar.
	return false, fmt.Sprintf("capability %s negada por default-deny (sem permit aplicavel; camada=cedar)", in.Capability), nil
}

// smokeProbeRule corre uma AVALIAÇÃO DE FUMO sobre UMA regra Cedar (por @id), garantindo
// que a avaliação ALCANÇA o motor e devolve erro (ErrMalformedRequest) se a regra referir um
// atributo FORA do mapa FIXO de entidades/contexto que [cedarEngine.evaluate] monta
// (principal.authority, resource.region, context.taint, context.sensitivity). É o defeito
// que [SignBundle]/policy-sign de outro modo assinariam e declarariam verde: uma regra que
// COMPILA mas cujo `when`, em runtime, dá diag.Errors ⇒ deny de tudo (contrato C1).
//
// COMO ALCANÇA O MOTOR (e porque não passa por [PDP.Decide]): Decide corre o gate default-deny
// da ALLOWLIST ANTES do Cedar; um input cuja capability não esteja na allowlist da classe é
// negado pela allowlist e NUNCA chega ao motor — a sonda seria um falso-negativo. Esta sonda
// avalia DIRECTAMENTE contra a policy set (via i do AOS-378), exercitando o mapa de atributos
// sem depender da allowlist. Para o motor avaliar mesmo o `when` da regra (e não curto-circuitar
// por scope que não casa), o request:
//   - usa a ACÇÃO que a regra restringe (extraída por [smokeActionHeadRe]);
//   - semeia `authority` com essa mesma capability e usa region="eu"/taint="trusted", de modo
//     a SATISFAZER os guardas típicos (`authority.contains(cap)`, `region == "eu"`, `taint !=
//     "untrusted"`) e alcançar o resto do `when`. Como os atributos MAPEADOS nunca provocam
//     erro (seja qual for o valor), estes valores não geram falsos positivos nas regras reais;
//     só um atributo NÃO mapeado erra.
func (e *cedarEngine) smokeProbeRule(id string) error {
	pol := e.policies.Get(cedar.PolicyID(id))
	if pol == nil {
		// Regra desaparecida entre a enumeração e a sonda (hot-reload concorrente); nada a
		// sondar aqui — a próxima enumeração vê a policy set nova.
		return nil
	}
	marshaled := pol.MarshalCedar()

	// GARANTIA ESTÁTICA (a que conta): um atributo fora do mapa fixo é apanhado aqui,
	// independentemente de a sonda dinâmica alcançar ou não o `when` da regra. Sem isto,
	// `action in [...]` e um atributo mau atrás de um `&&` sobre valor não-semeado passariam
	// por verde e fariam deny-all silencioso em runtime (achado da revisão adversarial).
	if fora := attrRefsForaDoMapa(marshaled); len(fora) > 0 {
		return fmt.Errorf("%w: avaliacao de fumo (estatica) da regra %q: referencia atributo(s) fora do mapa fixo do motor (%s) — so principal.authority, resource.region, context.taint e context.sensitivity existem em runtime; a regra compila e assina mas em producao da ErrMalformedRequest (deny-all da capability)",
			ErrMalformedRequest, id, strings.Join(fora, ", "))
	}

	action := "cap:aos.smoke-probe" // sem `action ==`: a sonda dinâmica casa qualquer acção
	if m := smokeActionHeadRe.FindSubmatch(marshaled); m != nil {
		action = string(m[1])
	}

	principalUID := cedar.NewEntityUID(entityPrincipal, cedar.String("aos.smoke-principal"))
	resourceUID := cedar.NewEntityUID(entityResource, cedar.String("aos.smoke-resource"))
	entities := cedar.EntityMap{
		principalUID: cedar.Entity{
			UID:        principalUID,
			Attributes: cedar.NewRecord(cedar.RecordMap{"authority": cedar.NewSet(cedar.String(action))}),
		},
		resourceUID: cedar.Entity{
			UID:        resourceUID,
			Attributes: cedar.NewRecord(cedar.RecordMap{"region": cedar.String("eu")}),
		},
	}
	single := cedar.NewPolicySet()
	single.Add(cedar.PolicyID(id), pol)
	req := cedar.Request{
		Principal: principalUID,
		Action:    cedar.NewEntityUID(entityAction, cedar.String(action)),
		Resource:  resourceUID,
		Context: cedar.NewRecord(cedar.RecordMap{
			"taint":       cedar.String("trusted"),
			"sensitivity": cedar.String("public"),
		}),
	}

	_, diag := cedar.Authorize(single, entities, req)
	if len(diag.Errors) > 0 {
		return fmt.Errorf("%w: regra %q referencia atributo fora do mapa fixo do motor (avaliacao de fumo falhou): %s",
			ErrMalformedRequest, id, diag.Errors[0].Message)
	}
	return nil
}

// obligationsFor deriva as obrigações de um permit, coerentes com as regras
// `obligations contains { ... } if { allow; ... }` de tecnica/12 §9:
//   - audit(level=full) SEMPRE que a decisão é permit;
//   - redact_pii(email, phone) quando o contexto é sensitivity == "confidential".
//
// A ordem é determinística (redact_pii antes de audit, como no exemplo de
// response do contrato C1) para testes golden estáveis.
//
// MAPEAMENTO CANÓNICO C1. O golden Rego de §9 emite {"type":"audit","level":"full"}
// com `level` ao nível de topo da obligation. O modelo [Obligation] (partilhado
// com o RM: Type/Fields/Params) não tem campo de topo arbitrário, pelo que
// `level` é transportado CANONICAMENTE em params.level → JSON
// {"type":"audit","params":{"level":"full"}}. Esta é a representação de contrato
// (C1) do PDP: o PEP lê o nível de audit de params.level. redact_pii coincide
// exactamente com §9 (usa Fields). Manter este mapeamento estável evita deriva
// PEP; qualquer alteração à forma da obligation é MAJOR no contrato de porta.
func obligationsFor(in Input) []Obligation {
	obs := make([]Obligation, 0, 2)
	if in.Context.Sensitivity == "confidential" {
		obs = append(obs, Obligation{Type: "redact_pii", Fields: []string{"email", "phone"}})
	}
	obs = append(obs, Obligation{Type: "audit", Params: map[string]string{"level": "full"}})
	return obs
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
