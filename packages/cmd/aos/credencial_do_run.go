package main

// credencial_do_run.go — A CREDENCIAL DO RUN VERIFICA-SE NA PORTA (AOS-428).
//
// # A REGRA É UMA, E VIVE NO `identity.Verifier`
//
// Esta guarda NÃO reimplementa a verificação: chama o MESMO `h.node.Verifier` que o hook
// `identity` do Reference Monitor usa. A razão é a lição que o AOS-424 passou uma série inteira
// a pagar — uma regra em três cópias, que derivaram, e um defeito que voltou a meio do mesmo
// ticket. Uma segunda implementação da verificação de identidade seria a mesma classe de
// defeito, num eixo pior.
//
// O precedente de chamar o verificador FORA do RM já existe e é do mesmo package: `resume.go`
// fá-lo para comparar o agente da credencial com o do run suspenso.
//
// # O QUE ESTE FICHEIRO **NÃO** FECHA, E UMA AFIRMAÇÃO QUE ESTEVE AQUI ERRADA
//
// Uma versão anterior deste comentário dizia que isto fechava o residual declarado em
// `resume.go` — «uma credencial que não verifica de todo não é recusada aqui». **NÃO FECHA**, e
// a revisão adversarial mediu-o: o `resume` faz `if p, verr := v.Verify(...); verr == nil && …`,
// pelo que quando a verificação FALHA o ramo inteiro é saltado e a retoma prossegue. Uma
// credencial malformada, expirada ou revogada re-hospeda um run, toma lease e consome plano de
// replay — todos os danos que este ticket enumera, menos a criação.
//
// O `POST /runs/{id}/resume` continua com o defeito. Está declarado como resíduo no AOS-428, e
// não se corrige aqui porque a retoma tem um eixo próprio: lá o `AgentID` do token e o do run
// suspenso são ambos agentes e comparam-se, o que aqui não acontece.

import (
	"context"
	"errors"
	"strings"

	"github.com/aos-ref/platform/identity"
)

// credencialDoRunRecusada devolve a razão da recusa, ou "" se a credencial passar.
//
// A razão é para o LOG DO OPERADOR. A resposta HTTP é uniforme — ver o chamador.
//
// # O QUE ESTA FUNÇÃO NÃO DECIDE
//
// Não decide se a credencial é a CERTA para este run — só se é uma credencial válida. A ligação
// entre o token e quem o apresenta (o eixo `AgentID` vs `PrincipalNHI`) é outro problema: em
// modo soberano o `PrincipalNHI` é sobrescrito pelo principal OIDC do submissor, que é um
// HUMANO, enquanto o `AgentID` do token é a NHI de um agente. São eixos diferentes e uma
// igualdade directa falharia. O `resume.go` faz essa comparação porque lá os dois lados são
// agentes; aqui não são. Fica FORA de âmbito e declarado no AOS-428.
func (h *apiHandler) credencialDoRunRecusada(ctx context.Context, credencial string) string {
	// SEM VERIFICADOR NÃO SE RECUSA, e isto não é uma porta aberta: o `Bootstrap` ABORTA se não
	// conseguir compor um verificador — nem o ramo endurecido (trust anchor) nem o de referência
	// (autoridade co-localizada) deixam este campo a nil. O ramo existe porque um `apiHandler`
	// montado à mão num teste pode não ter nó composto, e nesse caso a verificação de jusante
	// (o `rmadapter`, que é fail-closed com verificador nil) continua a ser a rede.
	if h.node == nil || h.node.Verifier == nil {
		return ""
	}
	// A CREDENCIAL AUSENTE SÓ SE RECUSA EM MODO ENDURECIDO, E A RAZÃO FOI MEDIDA PELO SMOKE.
	//
	// Recusá-la SEMPRE parecia óbvio — um run que não pode fazer nada mediado é um run que vai
	// morrer. E partiu o nó de referência: o smoke deixou de conseguir submeter, e não por
	// descuido da fixture. **Em modo NÃO-endurecido não existe forma de um cliente externo
	// obter uma credencial**: a autoridade de emissão é co-localizada no processo e não tem
	// rota de emissão. Exigir presença ali é exigir uma coisa que o nó não sabe dar.
	//
	// Em modo ENDURECIDO a situação é a oposta: o nó não tem autoridade nenhuma (trust-anchor
	// only, AOS-156), a credencial vem sempre de fora, e `AOS_MODE=production` exige
	// `AOS_ISSUER_PUBKEY` — logo produção é sempre endurecida e a presença é sempre exigível.
	//
	// O predicado é `Authority == nil`, e não uma bandeira nova: é o composition-root que decide
	// os dois ramos, e o ramo endurecido é exactamente aquele em que não se compõe autoridade.
	//
	// RESÍDUO DECLARADO no AOS-428: em modo de referência um run sem credencial continua a ser
	// criado e a morrer no RM com `denied_by=identity`. Fica por fechar de propósito, e o preço
	// está escrito aqui em vez de ser descoberto.
	if strings.TrimSpace(credencial) == "" {
		if h.node.Authority == nil {
			return "credencial ausente"
		}
		return ""
	}
	if _, err := h.node.Verifier.Verify(ctx, credencial); err != nil {
		return nomeDaRecusaDeCredencial(err)
	}
	return ""
}

// nomeDaRecusaDeCredencial traduz a sentinela do verificador num nome para o log.
//
// Existe para que o operador leia «expirada» em vez de um erro embrulhado, sem que a distinção
// chegue ao wire. É a mesma separação que o read-path soberano já faz — a resposta é uniforme,
// o log é específico.
//
// O `default` devolve o erro tal e qual: uma sentinela nova que ninguém mapeie aqui fica
// legível na mesma, em vez de virar «desconhecido» e perder a informação.
func nomeDaRecusaDeCredencial(err error) string {
	switch {
	case errors.Is(err, identity.ErrTokenExpired):
		return "expirada"
	case errors.Is(err, identity.ErrTokenNotYetValid):
		return "ainda nao valida (nbf no futuro)"
	case errors.Is(err, identity.ErrTokenRevoked):
		// «OU O REGISTO NÃO RESPONDEU», e a ambiguidade é do verificador, não desta linha: ele
		// embrulha o erro de CONSULTA na mesma sentinela da revogação genuína, com `%v` e não
		// `%w`, pelo que daqui não se distinguem. Num incidente do registo de revogação, TODAS as
		// submissões seriam recusadas e o log diria «revogada» para todos os titulares — o
		// diagnóstico apontaria para o sítio errado precisamente quando isso custa mais.
		//
		// RESÍDUO DECLARADO no AOS-428: a distinção exige uma sentinela própria no
		// `identity.Verifier`, que é outro módulo. Enquanto não existir, a mensagem não mente.
		return "revogada ou registo de revogacao indisponivel"
	case errors.Is(err, identity.ErrUnknownIssuer):
		return "emissor desconhecido (nao esta no trust anchor deste no)"
	case errors.Is(err, identity.ErrSignatureInvalid):
		return "assinatura invalida"
	case errors.Is(err, identity.ErrTokenMalformed):
		return "malformada"
	case errors.Is(err, identity.ErrUnsupportedAlg):
		return "algoritmo nao suportado"
	case errors.Is(err, identity.ErrDelegationInvalid):
		return "cadeia de delegacao invalida"
	default:
		return err.Error()
	}
}
