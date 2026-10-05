// AOS-496 — O APAGAMENTO DO TITULAR CHEGA AO REGISTO DE DESFECHOS EM MEMÓRIA.
//
// O `GET /runs/{id}` serve um run concluído por dois ramos: o registo de desfechos em memória
// ([NodeService.Outcome]), que guarda o `final_text` em claro, e o ramo durável, que o decifra
// da captura do turno terminal ([apiHandler.saidaDuravel]). O crypto-shred destrói a KEK do
// titular: o ramo durável e o `/reconstruct` deixam de abrir o conteúdo. O registo em memória
// não depende da KEK, e continuava a servir o texto até o nó reiniciar ou a poda FIFO o levar.
//
// A correcção não acrescenta uma segunda forma de responder «apagado»: o desfecho do titular SAI
// do registo em memória, e o `GET` cai no ramo durável — que já responde o que o log permite
// (`output_unavailable` com a KEK destruída). É o mesmo efeito de um reinício, que o nó já
// suporta em todos os caminhos que consultam o registo.
package main

import "sync"

// avisoDeTitularApagado leva a quem guarda conteúdo de titular EM MEMÓRIA a notícia de que a KEK
// de um titular foi mandada destruir. Existe porque as duas vias de destruição — o
// `/dsar/erase` ([apiHandler.handleDSAR]) e a expiração por TTL ([cryptoShredSink.Expire]) — são
// compostas no [Bootstrap], antes de o [NodeService] existir.
//
// O aviso dá-se quando a destruição é PEDIDA, confirmada ou não pela custódia: esquecer uma
// cópia em claro nunca é o lado inseguro, e o ramo durável continua a responder pelo log.
//
// Um ponteiro nil não avisa ninguém (um [Node] montado à mão num teste).
type avisoDeTitularApagado struct {
	mu       sync.Mutex
	ouvintes []func(titular string)
}

// subscrever regista um ouvinte. Corre fora do lock do aviso, na goroutine de quem avisa.
func (a *avisoDeTitularApagado) subscrever(f func(titular string)) {
	if a == nil || f == nil {
		return
	}
	a.mu.Lock()
	a.ouvintes = append(a.ouvintes, f)
	a.mu.Unlock()
}

// avisar chama cada ouvinte com o titular. Um titular vazio não é titular nenhum.
func (a *avisoDeTitularApagado) avisar(titular string) {
	if a == nil || titular == "" {
		return
	}
	a.mu.Lock()
	ouvintes := append([]func(string){}, a.ouvintes...)
	a.mu.Unlock()
	for _, f := range ouvintes {
		f(titular)
	}
}

// esquecerDesfechosDoTitular retira do registo de terminados os desfechos dos runs cujo titular
// é `titular`, e devolve quantos saíram. O titular de cada run é o que a captura usou para o
// cifrar ([agentruntime.Goal.Titular]), guardado no [runState] à entrada.
//
// Só o balde de TERMINADOS: é o único de onde o `GET /runs/{id}` serve conteúdo do run. Um run
// suspenso ou em curso responde com estado e pendentes, e tirá-lo do registo perdia a posse.
func (s *NodeService) esquecerDesfechosDoTitular(titular string) int {
	if titular == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fica := s.completedOrder[:0]
	saiu := 0
	for _, id := range s.completedOrder {
		if rs, ok := s.completed[id]; ok && rs.titular == titular {
			delete(s.completed, id)
			saiu++
			continue
		}
		fica = append(fica, id)
	}
	// Limpa a cauda do array de suporte: sem isto os RunIDs retirados ficavam vivos nele.
	for i := len(fica); i < len(s.completedOrder); i++ {
		s.completedOrder[i] = ""
	}
	s.completedOrder = fica
	return saiu
}
