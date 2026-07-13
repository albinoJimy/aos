// Package projection materializa o lado CONTEXTO do Princípio 4 (contexto ≠
// registo, AOS-036): a projecção que produz o que o MODELO vê — um resumo
// higienizado e LIMITADO EM TOKENS. É a via fisicamente separada da via de registo
// (pacote record):
//
//   - ProjectContext recebe apenas uma vista READ-ONLY do registo (record.RecordView)
//     e NUNCA lhe pode escrever/apagar — a barreira é a nível de tipo (a interface
//     não expõe mutadores). Higienizar/descartar aqui é economia legítima;
//   - a trajectória completa (conteúdo cru + árvore de spans) vai sempre para o
//     backend pela via record.Persist, intacta.
//
// A projecção é DETERMINÍSTICA e REPRODUZÍVEL: a mesma trajectória + a mesma
// política versionada produzem a MESMA injecção byte-a-byte. Não há time.Now nem
// rand — a estimativa de tokens é uma função pura do texto.
package projection

import (
	"strconv"
	"strings"
)

// DefaultPolicyVersion é a versão SemVer da política de projecção default. A
// política é versionada (SemVer, à imagem da política de projecção do AOS-030):
// reprodutibilidade exige que a injecção seja função da trajectória E da versão da
// política — mudá-la é uma alteração observável e versionada, nunca silenciosa.
const DefaultPolicyVersion = "1.0.0"

// DefaultTokenBudget é o orçamento de tokens default do resumo do sub-agente ao pai
// (~1–2k tokens, configurável). O resumo entregue ao contexto do pai nunca o
// excede; a árvore de spans completa vai à mesma para o backend.
const DefaultTokenBudget = 2000

// Policy é a POLÍTICA de projecção versionada. Governa como a trajectória é
// projectada para o contexto do modelo: o orçamento de tokens do resumo e o
// separador entre turnos. É versionada em SemVer — a mesma trajectória sob a mesma
// Policy produz sempre a mesma injecção.
type Policy struct {
	// Version é a versão SemVer da política (obrigatória, validada).
	Version string
	// TokenBudget é o tecto do resumo em tokens (> 0). Turnos que não caibam são
	// descartados do CONTEXTO (legítimo) — nunca do registo.
	TokenBudget int
	// Separator é o separador determinístico entre resumos de turnos.
	Separator string
}

// DefaultPolicy devolve a política default (v1.0.0, orçamento ~2k tokens).
func DefaultPolicy() Policy {
	return Policy{
		Version:     DefaultPolicyVersion,
		TokenBudget: DefaultTokenBudget,
		Separator:   "\n",
	}
}

// WithTokenBudget devolve uma cópia da política com o orçamento de tokens dado.
// O orçamento é configurável mantendo a versão da política (o mesmo layout).
func (p Policy) WithTokenBudget(budget int) Policy {
	p.TokenBudget = budget
	return p
}

// Validate impõe uma política bem-formada (fail-closed): versão SemVer válida e
// orçamento de tokens positivo. Uma política inválida rejeita a projecção — nunca
// há default silencioso a meio da hot path.
func (p Policy) Validate() error {
	if !validSemVer(p.Version) {
		return ErrInvalidPolicyVersion
	}
	if p.TokenBudget <= 0 {
		return ErrInvalidTokenBudget
	}
	return nil
}

// validSemVer aceita um "MAJOR.MINOR.PATCH" numérico simples (sem pré-lançamento).
// É deliberadamente restrito: a política de projecção só precisa de SemVer básico e
// evita puxar dependências de parsing.
func validSemVer(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}
