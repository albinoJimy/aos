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
// **FECHADO EM AOS-433.** O `POST /runs/{id}/resume` passou a chamar esta mesma regra, por
// `credencialDoRunRecusadaNoNo`, antes da comparação de principal que ele já fazia. O eixo
// próprio da retoma — comparar o `AgentID` do token com o do run suspenso — mantém-se e continua
// a correr a seguir; o que faltava era recusar a credencial que não verifica DE TODO.

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
	return credencialDoRunRecusadaNoNo(ctx, h.node, credencial)
}

// credencialDoRunRecusadaNoNo e a MESMA regra, sobre o no, para quem nao tem `apiHandler`.
//
// Passou a funcao livre em AOS-433, quando o `POST /runs/{id}/resume` precisou dela. A
// alternativa era escrever a verificacao uma segunda vez no `resume`, e este eixo ja pagou
// caro por isso: o AOS-424 encontrou a regra do `stream_id` em TRES copias derivadas, e o
// AOS-429 recusou duplicar a definicao de «terminado» pela mesma razao.
//
// Uma regra, uma fonte, dois chamadores.
func credencialDoRunRecusadaNoNo(ctx context.Context, no *Node, credencial string) string {
	// SEM VERIFICADOR NÃO SE RECUSA, e isto não é uma porta aberta: o `Bootstrap` ABORTA se não
	// conseguir compor um verificador — nem o ramo endurecido (trust anchor) nem o de referência
	// (autoridade co-localizada) deixam este campo a nil. O ramo existe porque um `apiHandler`
	// montado à mão num teste pode não ter nó composto, e nesse caso a verificação de jusante
	// (o `rmadapter`, que é fail-closed com verificador nil) continua a ser a rede.
	if no == nil || no.Verifier == nil {
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
		if no.Authority == nil {
			return "credencial ausente"
		}
		return ""
	}
	if _, err := no.Verifier.Verify(ctx, credencial); err != nil {
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
	case errors.Is(err, identity.ErrRevocationUnavailable):
		// A DISTINÇÃO QUE FALTAVA (AOS-433). Antes, isto e a revogação genuína resolviam na
		// mesma sentinela e o log dizia «revogada» às duas. Numa avaria do registo, todos os
		// titulares eram recusados e o operador ia revogar identidades por causa de um serviço
		// em baixo. Agora o log nomeia a causa, e a causa subjacente viaja no erro.
		return "registo de revogacao INDISPONIVEL (nao e revogacao: fail-closed por nao se poder verificar)"
	case errors.Is(err, identity.ErrTokenRevoked):
		// REVOGAÇÃO GENUÍNA, e agora quer mesmo dizer isso (AOS-433). Até então esta linha
		// tinha de se desculpar: o verificador embrulhava o erro de CONSULTA na mesma sentinela,
		// com `%v` e não `%w`, e daqui não se distinguiam. A distinção passou a existir na
		// fonte, que é onde tinha de estar.
		return "revogada"
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
