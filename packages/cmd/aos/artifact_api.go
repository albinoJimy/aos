package main

// AS ROTAS DO ARMAZÉM DE ARTEFACTOS (AOS-548, ADR-043 §2.3).
//
//   - GET /runs/{id}/artifacts            a lista dos artefactos do run, tirada dos eventos
//     `artifact.created` do stream dele (sem o conteúdo);
//   - GET /runs/{id}/artifacts/{digest}   o conteúdo de UM artefacto, decifrado.
//
// AS DUAS PASSAM PELAS PORTAS DA LEITURA DE CONTEÚDO ([apiHandler.portasDaLeituraDeConteudo]), as
// mesmas do `/reconstruct` e do `/replay`, pela mesma ordem: soberania do leitor (404 uniforme),
// composição (501), posse do stream com a trava dos streams internos (404 uniforme) e o selo da
// leitura no WORM como pré-condição (503), com a capability [capReadArtifact].
//
// SÓ UM DIGEST QUE O PRÓPRIO RUN DECLARA. O conteúdo serve-se só se um `artifact.created` do stream
// do run o declara: um ficheiro na pasta sem evento, ou o digest de outro run, dá o mesmo 404. O
// titular com que se decifra é o do evento — o do run —, e os bytes decifrados voltam a conferir
// contra o digest. Apagado (a KEK do titular destruída) ⇒ 410.
//
// ENTREGA SEMPRE COMO ANEXO. O conteúdo foi escrito pelo modelo (untrusted): `Content-Disposition:
// attachment`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src
// 'none'` e `Cache-Control: no-store`. Um SVG aberto num browser por engano corre numa sandbox sem
// origem e sem rede.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/aos-ref/substrate/eventstore"
)

// capReadArtifact é a capability selada no WORM (D6) para a leitura dos artefactos de um run.
const capReadArtifact = "read:artifact"

// artefactosDoRun devolve os artefactos que os eventos do stream declaram, pela ordem dos eventos.
// Um evento ilegível fica de fora (não se serve o que não se sabe ler).
func artefactosDoRun(events []eventstore.Event) []artefactoCriado {
	var out []artefactoCriado
	for _, e := range events {
		// Só o evento do PRÓPRIO nó, pela identidade emissora do armazém (achado 3 da revisão).
		if e.Type != EventTypeArtifactCreated || e.Producer.NHIID != artifactNHI {
			continue
		}
		var p artefactoCriado
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.Digest == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// leituraDeArtefactos são as portas da leitura de conteúdo para estas rotas: sem armazém composto,
// 501 antes da posse e do selo.
func (h *apiHandler) leituraDeArtefactos() leituraDeConteudo {
	return leituraDeConteudo{
		desligada: "artefactos desligados", indisponivel: "artefactos indisponiveis", capability: capReadArtifact,
		composta: func(w http.ResponseWriter) bool {
			if h.node.artefactos == nil {
				writeError(w, http.StatusNotImplemented, "artefactos desligados (AOS_ARTIFACT_DIR por definir)")
				return false
			}
			return true
		},
	}
}

// itemDaLista é uma entrada de GET /runs/{id}/artifacts: o que o evento declara, sem o titular.
type itemDaLista struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
	Name      string `json:"name"`
	StepID    string `json:"step_id"`
}

// handleArtifacts serve GET /runs/{id}/artifacts.
func (h *apiHandler) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	_, events, ok := h.portasDaLeituraDeConteudo(w, r, runID, h.leituraDeArtefactos())
	if !ok {
		return
	}
	lista := []itemDaLista{}
	for _, a := range artefactosDoRun(events) {
		lista = append(lista, itemDaLista{Digest: a.Digest, MediaType: a.MediaType, Bytes: a.Bytes, Name: a.Name, StepID: a.StepID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "artifacts": lista})
}

// handleArtifact serve GET /runs/{id}/artifacts/{digest}.
func (h *apiHandler) handleArtifact(w http.ResponseWriter, r *http.Request) {
	runID, digest := r.PathValue("id"), r.PathValue("digest")
	_, events, ok := h.portasDaLeituraDeConteudo(w, r, runID, h.leituraDeArtefactos())
	if !ok {
		return
	}
	var achado *artefactoCriado
	for _, a := range artefactosDoRun(events) {
		if a.Digest == digest {
			a := a
			achado = &a
			break
		}
	}
	if achado == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	conteudo, err := h.node.artefactos.abrir(r.Context(), achado.SealedSubject, runID, digest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeError(w, reconstructErrorStatus(err), "artefacto indisponivel")
		return
	}
	h.node.artefactos.servidos.Add(1)
	nome := achado.Name
	if !nomeDeArtefacto.MatchString(nome) {
		nome = "artefacto"
	}
	cab := w.Header()
	tipo := achado.MediaType
	if !tiposDeArtefacto[tipo] {
		tipo = "application/octet-stream" // o tipo do evento revalida-se contra o vocabulário
	}
	cab.Set("Content-Type", tipo)
	cab.Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(nome, `"`, "")+`"`)
	cab.Set("X-Content-Type-Options", "nosniff")
	cab.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	cab.Set("Cache-Control", "no-store")
	cab.Set("Content-Length", strconv.Itoa(len(conteudo)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(conteudo)
}
