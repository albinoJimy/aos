package main

// A AUTORIDADE DO QUE SE REIDRATA DO WORM (achado de revisão sobre AOS-307).
//
// O DEFEITO QUE ISTO FECHA. AOS-307 fez o arranque REIDRATAR os níveis de autonomia a
// partir da partição `autonomy` do WORM, para que uma decisão assinada por um operador
// sobrevivesse a um reinício. Isso é correcto — e transformou o WORM numa ENTRADA
// AUTORITATIVA DE PRIVILÉGIO. O problema é o que sustentava essa autoridade:
//
//   - `LevelRegistry.Rehydrate` filtrava `autonomy.level_changed` e aplicava. Validava que
//     os params existiam e que os níveis parseavam. NÃO confrontava o `actor` com nada.
//   - `EntryHash = SHA-256(prevHash ‖ conteúdo)` — SEM CHAVE. Encadear um registo forjado
//     é aritmética pública, e `audit.VerifyStore`, que corre antes no [Bootstrap], só
//     RE-ENCADEIA: um append bem-formado passa.
//   - Mesmo com `AOS_WORM_ANCHOR`, a verificação ancorada corre até `cp.AuditSeq` — o
//     último checkpoint. Um registo APENDIDO DEPOIS fica FORA do intervalo verificado.
//
// Medido: quem escreve no ficheiro do WORM apende um `autonomy.level_changed` com
// `actor:"op:qualquer"` e `new_level:L5`, reinicia o nó, e o par passa a servir L5 — sem
// uma única assinatura, contornando o dual-control que AOS-305 acabou de instalar.
//
// O QUE PASSA A VALER. O registo transporta a(s) ASSINATURA(S) que o produziram
// ([autonomy.LevelChangeProof], seladas nos Params, logo dentro do EntryHash) e a
// rehidratação RECUSA o que não verificar contra uma raiz de confiança que NÃO vive no
// store: as pubkeys de `AOS_OPERATORS`, que o nó recebe do ambiente. Um registo que não
// verifica ABORTA o arranque nomeando o `AuditSeq` — o mesmo fail-closed que já existia
// para um registo malformado.
//
// O QUE ISTO CUSTA, e é honesto dizê-lo: um WORM escrito por uma versão ANTERIOR tem
// registos de operador SEM prova — foram selados antes de o campo existir. Um nó que os
// releia com este validador ABORTA o arranque. É a direcção segura e não há outra: aceitar
// registos sem prova «por serem antigos» seria manter aberta exactamente a porta que isto
// fecha, e um adversário só teria de omitir a prova para entrar por ela. A saída é do
// operador e é deliberada: reassinar a decisão por `POST /autonomy` (que passa a selar com
// prova), ou remover a partição `autonomy` do WORM e reprovisionar por
// AOS_AUTONOMY_LEVELS — em qualquer dos casos, uma pessoa decide, em vez de o nó decidir
// por omissão.
//
// PORQUE A REGRA VIVE AQUI e não no pacote `autonomy`: verificar exige o tuplo canónico
// do canal de controlo (packages/integration) e o registo de pubkeys do nó. Um pacote de
// governação a importar a camada de integração inverteria a fronteira de camadas, e o
// gate `scripts/ci/layer-lint.sh` recusa-o. `cmd/aos` já importa os dois — é o
// composition-root, é o sítio onde a política de confiança do nó se escreve.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// ErrAutonomyRehydrateUnverified — um `autonomy.level_changed` relido do WORM não trouxe
// prova assinada suficiente para a autoridade que reclama. Fail-closed NO NÍVEL: o registo é
// SALTADO (nunca aplicado) e declarado no banner com o seq; o arranque prossegue, porque
// abortar por um registo entregaria um modo de tijolo a quem escreve no ficheiro do WORM.
//
// A mensagem é DELIBERADAMENTE explícita (ao contrário do 403 uniforme da rota): aqui não
// há adversário remoto a sondar respostas — há um operador em frente a um nó que não
// arranca, e o que ele precisa de saber é qual o registo e o que lhe falta.
var ErrAutonomyRehydrateUnverified = errors.New("aos: alteracao de nivel de autonomia RELIDA do WORM sem prova assinada valida — desde AOS-307 o WORM e autoritativo sobre os niveis no arranque, e o EntryHash da hash-chain e um SHA-256 SEM CHAVE (quem escreve no ficheiro consegue apendir um registo que a re-verificacao aceita); por isso um registo de OPERADOR so e reidratado se trouxer a(s) assinatura(s) ed25519 do pedido que o originou, verificaveis contra AOS_OPERATORS e emitidas por quem detem autonomy:set (AOS_AUTONOMY_SETTERS)")

// autonomyRehydrateValidator constrói o predicado que o [Bootstrap] injecta em
// [autonomy.WithRehydrateValidator]. `operators` é emitterID→pubkey (Config.Operators) e
// `setters` o conjunto com `autonomy:set` (já validado ⊆ operators pelo Bootstrap).
//
// AS REGRAS, e a razão de cada uma:
//
//	(1) actor == "config:node" ⇒ ACEITE SEM ASSINATURA. Não há assinatura para exigir: o
//	    provisionamento é a configuração do deployment a aplicar-se, e ninguém a assina.
//	    É seguro pela PRECEDÊNCIA que AOS-307 já implementa — um par cujo último selo é de
//	    `config:node` CEDE a AOS_AUTONOMY_LEVELS. Forjar um registo destes não concede
//	    nada: o ambiente sobrepõe-se-lhe a seguir, no mesmo provision.
//
//	(2) QUALQUER OUTRO ACTOR ⇒ exige pelo menos uma prova cuja assinatura verifique, com a
//	    pubkey registada desse emissor, sobre o payload canónico EXACTO da alteração
//	    ([integration.CanonicalAutonomyPayload] com agente, domínio, nível NOVO e motivo),
//	    no âmbito [integration.AutonomyScope] / [control.SignalAutonomy]. É byte a byte o
//	    que a rota `POST /autonomy` faz assinar — se divergisse, um registo legítimo
//	    deixaria de verificar e a divergência apareceria como «assinatura inválida», que se
//	    lê como chave errada e manda procurar no sítio errado.
//
//	(3) O emissor tem de deter `autonomy:set` AGORA. A rota recusa um operador fora de
//	    AOS_AUTONOMY_SETTERS mesmo para L1; a rehidratação não pode aceitar o que a rota
//	    recusaria, senão o caminho mais fraco passa a ser o do reinício. E a autoridade
//	    confrontada é a de AGORA, não a de então: quem perdeu o direito perdeu-o também
//	    sobre o que assinou no passado — retirar um operador da lista é uma decisão de
//	    governação, e ela tem de ter efeito.
//
//	(4) New >= L4 ⇒ DUAS provas de emissores DISTINTOS, ambas verificadas e ambas com
//	    `autonomy:set`. É exactamente [autonomyDualControlRequired], a regra que a rota
//	    impõe no limiar em que `danger` deixa de esperar por um humano. Sem isto, a
//	    rehidratação seria a porta das traseiras do dual-control de AOS-305.
//
// operators/setters vazios ⇒ NENHUM registo de operador se reidrata (só os de
// `config:node`). É a leitura honesta do fail-closed: um nó sem pubkeys de operador não
// tem como confirmar decisão nenhuma, e servir um nível que não confirma é o defeito.
//
// UM ACTOR QUE AINDA NÃO EXISTE NO WORM DESTE NÓ, e que fica declarado para não ser uma
// surpresa: [autonomy.ControllerActor] ("autonomy-controller"), das democões automáticas
// por anomalia. Hoje o [autonomy.Controller] NÃO tem chamador em cmd/aos, pelo que nenhum
// registo desses chega a esta partição. Quando tiver, cai na regra (2) e ABORTA o arranque
// — de propósito: aceitá-lo sem prova daria um terceiro actor forjável, e um forjador
// escolhe `new_level` (o controlador só desce; um registo forjado não é obrigado a
// descer). Fechá-lo exige dar ao controlador uma prova própria — uma assinatura do nó
// sobre a transição, com a chave do nó — e isso é trabalho a fazer com o controlador, não
// a antecipar aqui.
func autonomyRehydrateValidator(operators map[string]ed25519.PublicKey, setters map[string]bool) func(autonomy.LevelChange) error {
	return func(ch autonomy.LevelChange) error {
		// (1) Provisionamento por configuração: sem assinatura, e sem privilégio a ganhar.
		if ch.Actor == autonomyProvisionActor {
			return nil
		}

		exigidas := 1
		if autonomyDualControlRequired(ch.New) {
			exigidas = 2
		}
		payload := integration.CanonicalAutonomyPayload(ch.Agent, ch.Domain, ch.New.String(), ch.Reason)

		// Conjunto e não contador: «duas provas» tem de ser «duas pessoas». Duas assinaturas
		// do mesmo emissor (com nonces distintos, o que é trivial de produzir para quem tem
		// a chave) não são dual-control — é a mesma recusa que a rota faz a `co_emitter` igual
		// ao `emitter`.
		verificados := make(map[string]struct{}, len(ch.Proofs))
		motivos := make([]string, 0, len(ch.Proofs))
		for _, p := range ch.Proofs {
			if err := autonomyProofVerifies(p, payload, operators, setters); err != nil {
				motivos = append(motivos, fmt.Sprintf("%s: %v", p.EmitterID, err))
				continue
			}
			verificados[p.EmitterID] = struct{}{}
		}
		if len(verificados) >= exigidas {
			return nil
		}
		sort.Strings(motivos)
		detalhe := "nenhuma prova no registo"
		if len(motivos) > 0 {
			detalhe = strings.Join(motivos, "; ")
		}
		return fmt.Errorf("%w: actor %q, par %s:%s -> %s: %d prova(s) valida(s) de emissores distintos, exigidas %d [%s]",
			ErrAutonomyRehydrateUnverified, ch.Actor, ch.Agent, ch.Domain, ch.New, len(verificados), exigidas, detalhe)
	}
}

// autonomyProofVerifies decide se UMA prova conta. Ordem fail-closed: direito primeiro
// (é o mais barato e o mais restritivo), pubkey depois, assinatura no fim.
func autonomyProofVerifies(p autonomy.LevelChangeProof, payload []byte, operators map[string]ed25519.PublicKey, setters map[string]bool) error {
	if p.EmitterID == "" {
		return errors.New("emitter_id vazio")
	}
	if !setters[p.EmitterID] {
		return fmt.Errorf("nao detem %s em AOS_AUTONOMY_SETTERS", autonomySetCapability)
	}
	pub, ok := operators[p.EmitterID]
	if !ok {
		return errors.New("sem pubkey em AOS_OPERATORS")
	}
	em, err := autonomyEmitterFromProof(p)
	if err != nil {
		return err
	}
	// VERIFY-ONLY: sem consumo de nonce e sem frescura — ver
	// [integration.VerifyEmitterSignature] para o porquê de ambos estarem errados a reler
	// o passado.
	return integration.VerifyEmitterSignature(integration.AutonomyScope, control.SignalAutonomy, payload, pub, em)
}

// autonomyEmitterFromProof é o INVERSO de [autonomyProofFromEmitter]: reconstrói o
// [control.Emitter] a partir da forma selada. O nonce e o carimbo entram no tuplo
// assinado, pelo que têm de voltar byte a byte — daí serem guardados e não descartados.
func autonomyEmitterFromProof(p autonomy.LevelChangeProof) (control.Emitter, error) {
	sig, err := base64.StdEncoding.DecodeString(p.SignatureB64)
	if err != nil {
		return control.Emitter{}, fmt.Errorf("signature base64 malformada: %v", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(p.NonceB64)
	if err != nil {
		return control.Emitter{}, fmt.Errorf("nonce base64 malformado: %v", err)
	}
	at, err := time.Parse(time.RFC3339Nano, p.IssuedAtRFC3339)
	if err != nil {
		return control.Emitter{}, fmt.Errorf("issued_at malformado: %v", err)
	}
	return control.Emitter{ID: p.EmitterID, Signature: sig, Nonce: nonce, IssuedAt: at.UTC()}, nil
}

// autonomyProofFromEmitter traduz um [control.Emitter] JÁ AUTENTICADO na prova a selar.
//
// RFC3339 com NANOS de propósito: o tuplo assinado usa `issued_at.UnixNano()`, pelo que um
// formato de segundo inteiro perderia a precisão que a assinatura cobre e nenhuma prova
// voltaria a verificar. UTC pela mesma razão — é o que [integration.signedMessage] usa.
func autonomyProofFromEmitter(em control.Emitter) autonomy.LevelChangeProof {
	return autonomy.LevelChangeProof{
		EmitterID:       em.ID,
		SignatureB64:    base64.StdEncoding.EncodeToString(em.Signature),
		NonceB64:        base64.StdEncoding.EncodeToString(em.Nonce),
		IssuedAtRFC3339: em.IssuedAt.UTC().Format(time.RFC3339Nano),
	}
}
