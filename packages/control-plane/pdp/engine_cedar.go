package pdp

import (
	"fmt"
	"sort"

	cedar "github.com/cedar-policy/cedar-go"
)

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
	return &cedarEngine{policies: ps, version: version}, nil
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
	return false, fmt.Sprintf("capability %s negada por default-deny (sem permit aplicavel)", in.Capability), nil
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
