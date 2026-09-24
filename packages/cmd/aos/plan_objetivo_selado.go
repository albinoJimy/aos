package main

// plan_objetivo_selado.go — O OBJECTIVO DO UTILIZADOR PASSA A ESTAR CIFRADO EM REPOUSO (AOS-429).
//
// # O QUE ESTAVA EM CLARO, E ONDE
//
// O `POST /plans` gravava o objectivo — **texto livre escrito por uma pessoa** — inline e sem
// cifra no facto `planrequest.submitted`. Dali vai para o WAL e para os backups, fora do alcance
// do crypto-shredding por-titular, que é o mecanismo que o resto do sistema usa para o Art. 17.
// O `plan_ingress.go` já o declarava por escrito; o que faltava era fechá-lo.
//
// # NÃO SE ESCREVEU CRIPTO NENHUMA
//
// A maquinaria existe, está testada e **já está composta no nó**: `audit.SealContent` /
// `audit.OpenContent` (envelope AES-256-GCM, DEK fresca por registo embrulhada na KEK do
// titular), sobre o MESMO `Node.DSARVault` que sela o conteúdo dos runs — a captura de
// não-determinismo e o step-ledger. A fila passa a ter a postura que esse conteúdo já tem, em
// vez de uma postura própria.
//
// Consequência directa e desejada: um `/dsar/erase` sobre um titular torna ilegíveis também os
// objectivos que ele submeteu. É o ponto.
//
// # PORQUE É QUE O CONSUMIDOR NÃO PRECISA DA CHAVE
//
// Esta foi a pergunta que fez a decisão parecer cara, e a resposta estava no AOS-423: desde que
// a fila ganhou a rota de reclamação, o `aos-orq` **não lê o Event Store**. Reclama por
// `POST /plans/claim` e recebe o pedido no corpo da resposta. Quem lê e projecta é o nó.
//
// Logo o nó decifra server-side e entrega em claro pelo canal que já está autenticado e gatado
// pela soberania. Não há distribuição de chaves, e a forma do wire (`respostaDeReclamo`, que o
// `aos-orq` espelha em `node_client.go`) **não muda**. O que muda é só a forma em REPOUSO, que é
// interna ao nó.
//
// # QUANDO NÃO SE SELA, E PORQUÊ ISSO NÃO É UMA PORTA ABERTA
//
// Sela-se quando há TITULAR. Sem gate soberano composto, o `POST /plans` continua a aceitar
// pedidos e o payload não tem principal (`plan_ingress.go`: o bloco de soberania é `if
// h.readGov != nil`) — e sem titular não há KEK sob a qual selar. Nesse nó o objectivo fica em
// claro, como sempre esteve.
//
// Não se fecha isso com um fail-closed porque seria fechar a rota num nó de referência inteiro,
// e a lição é recente: no AOS-428 exigir credencial onde o nó não sabe dar uma partiu o smoke.
// Em produção o problema não existe — `AOS_MODE=production` exige o gate soberano, e a guarda
// `ErrProductionNeedsDurableKEK` recusa arrancar com substrato durável e custódia volátil.

import (
	"errors"
	"fmt"

	"github.com/aos-ref/platform/audit"
)

// errObjetivoIlegivel — o blob existe mas não abre.
//
// Não se confunde com «não estava selado»: um objectivo que nunca foi selado devolve o campo em
// claro; um que foi selado e não abre é uma FALHA, e a diferença é o que separa «este nó não
// cifra» de «a chave deste titular foi destruída».
var errObjetivoIlegivel = errors.New("objectivo do pedido nao e legivel")

// titularDoPedido devolve o titular sob o qual o objectivo é selado.
//
// É o `principal` do submissor, que o gate soberano verificou e que o `plan_ingress` já recusa
// vazio. Existe como função, e não como acesso directo ao campo, porque o titular é uma DECISÃO
// de governação registada no AOS-429 — quem a quiser mudar muda-a aqui, num sítio, e o
// compilador mostra-lhe todos os sítios que dela dependem.
func titularDoPedido(p planRequestPayload) string { return p.Principal }

// selarObjetivo cifra o objectivo sob a KEK do titular, quando há titular e custódia.
//
// Devolve o payload MODIFICADO. O contrato é exclusivo e o teste amarra-o: ou `Objective` está
// preenchido e `ObjetivoSelado` vazio, ou o inverso — nunca os dois, que deixaria o texto em
// claro ao lado do ciphertext e tornaria a cifra decorativa.
func selarObjetivo(node *Node, p planRequestPayload) (planRequestPayload, error) {
	titular := titularDoPedido(p)
	if node == nil || node.DSARVault == nil || titular == "" {
		// SEM CUSTÓDIA OU SEM TITULAR FICA EM CLARO, como sempre esteve. Ver o cabeçalho.
		return p, nil
	}
	selado, err := audit.SealContent(node.DSARVault, titular, []byte(p.Objective), nil)
	if err != nil {
		// FAIL-CLOSED. Com custódia composta e titular conhecido, uma falha a selar não se
		// degrada para «grava em claro»: isso seria a rota a decidir sozinha baixar a postura
		// de protecção de dados, e em silêncio. O chamador recusa o pedido.
		return p, fmt.Errorf("selar objectivo do pedido: %w", err)
	}
	p.ObjetivoSelado = selado
	p.Objective = "" // o texto em claro NÃO acompanha o ciphertext
	// LIGA TITULAR ↔ PARTIÇÃO. É isto que torna a fila alcançável pelo legal hold e pelo shred:
	// sem a ligação, o índice não sabe que este titular tem dados nesta partição, e um
	// `/dsar/erase` destruiria a KEK sem nunca saber que havia aqui o que tornar ilegível.
	if node.DSARIndex != nil {
		node.DSARIndex.Link(titular, planRequestStream)
	}
	return p, nil
}

// abrirObjetivo devolve o objectivo em claro para entregar ao consumidor.
//
// Um payload sem `ObjetivoSelado` devolve o campo em claro — é o pedido de um nó sem titular, ou
// um pedido anterior a este ticket. Não é erro: o log é append-only e os factos antigos ficam
// como foram escritos.
func abrirObjetivo(node *Node, p planRequestPayload) (string, error) {
	if len(p.ObjetivoSelado) == 0 {
		return p.Objective, nil
	}
	titular := titularDoPedido(p)
	if node == nil || node.DSARVault == nil || titular == "" {
		// HÁ CIPHERTEXT E NÃO HÁ COM QUE O ABRIR. Isto é diferente de não haver cifra, e por
		// isso é erro: devolver "" em silêncio faria o consumidor correr um plano com um
		// objectivo VAZIO, que é a falha calada que o ADR-028 manda não ter.
		return "", fmt.Errorf("%w: pedido selado e o no nao tem custodia ou titular", errObjetivoIlegivel)
	}
	claro, err := audit.OpenContent(node.DSARVault, titular, p.ObjetivoSelado)
	if err != nil {
		// A CAUSA MAIS PROVÁVEL É LEGÍTIMA: a KEK do titular foi destruída por um
		// `/dsar/erase`. O pedido deixa de ser executável, e é isso que o Art. 17 quer dizer.
		// O erro sobe para o chamador o distinguir de uma falha de substrato.
		return "", fmt.Errorf("%w: %v", errObjetivoIlegivel, err)
	}
	return string(claro), nil
}

// objetivoDeclaradoEmClaro diz se este payload guarda o objectivo legível por quem leia o log.
//
// Existe para o teste e para o banner: a postura do nó quanto a dados pessoais na fila tem de
// ser AFIRMÁVEL, não inferida de ler o código.
func objetivoDeclaradoEmClaro(p planRequestPayload) bool { return len(p.ObjetivoSelado) == 0 }
