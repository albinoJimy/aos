// Package wirefake são os PROVIDERS FALSOS DE WIRE do Model Gateway (AOS-508): um corpo de
// resposta de chat completions por cada variação conhecida entre providers, e um servidor
// `net/http` da stdlib que os devolve. Deterministas, sem rede externa e sem relógio.
//
// SÓ PARA TESTES. Vive em `internal/` do módulo do gateway, e não em `packages/testkit`, porque
// o `layer-lint` não deixa uma camada de produção importar o testkit — nem nos seus testes — e
// são os testes da porta e do gateway que precisam destes casos. Nenhum ficheiro que não seja de
// teste o importa (TestAOS508_NenhumCodigoDeProducaoImportaOsFalsos).
//
// Cada caso é um ficheiro versionado em `casos/<nome>.json`, escrito à mão: não há aqui dados de
// titular nem texto de produção.
package wirefake

import (
	"embed"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

//go:embed casos/*.json
var casos embed.FS

// Nomes devolve os nomes de todos os casos, por ordem alfabética.
func Nomes() []string {
	entradas, err := casos.ReadDir("casos")
	if err != nil {
		panic("wirefake: casos ilegiveis: " + err.Error())
	}
	var out []string
	for _, e := range entradas {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// Corpo devolve o corpo de resposta de um caso. Um nome que não existe é um erro de quem
// escreveu o teste, e entra em pânico.
func Corpo(nome string) []byte {
	b, err := casos.ReadFile("casos/" + nome + ".json")
	if err != nil {
		panic("wirefake: caso desconhecido: " + nome)
	}
	return b
}

// Pedido é um pedido que o falso recebeu.
type Pedido struct {
	Caminho string
	Corpo   []byte
}

// Servidor é um provider falso: responde a qualquer POST com o corpo do caso escolhido e guarda
// os pedidos que recebeu. O caso escolhe-se por [Servidor.Servir], ou pelo primeiro segmento do
// caminho (`/<caso>/v1/chat/completions`) quando o pedido o traz — é assim que um proxy à frente
// dele escolhe um caso por rota.
type Servidor struct {
	mu      sync.Mutex
	caso    string
	pedidos []Pedido
}

// NovoServidor devolve um falso que serve o caso dado.
func NovoServidor(caso string) *Servidor { return &Servidor{caso: caso} }

// Servir muda o caso que o falso devolve aos pedidos seguintes.
func (s *Servidor) Servir(caso string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caso = caso
}

// Pedidos devolve uma cópia dos pedidos recebidos, pela ordem.
func (s *Servidor) Pedidos() []Pedido {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Pedido(nil), s.pedidos...)
}

// ServeHTTP implementa [http.Handler].
func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s.mu.Lock()
	s.pedidos = append(s.pedidos, Pedido{Caminho: r.URL.Path, Corpo: corpo})
	caso := s.caso
	s.mu.Unlock()
	if seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]; seg != "" && seg != "v1" && seg != "chat" {
		caso = seg
	}
	b, err := casos.ReadFile("casos/" + caso + ".json")
	if err != nil {
		http.Error(w, `{"error":{"message":"caso desconhecido"}}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
