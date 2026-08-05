// Command aos-attestation é o COMPONENTE DE AUTORIDADE EXTERNO da verificação de attestation de
// dispositivo WebAuthn (AOS-177). Corre o descodificador CBOR/COSE (packages/platform/attestation)
// que o binário do nó NUNCA pode importar (ADR-017): o nó fala com ele por HTTP stdlib
// (integration.RemoteDeviceAttestationVerifier). Bytes-in/bytes-out — nenhum tipo WebAuthn/CBOR
// atravessa a fronteira do nó.
//
// Modos:
//
//	serve     — HTTP POST /verify: {attestation_object, client_data_json, expected_challenge}
//	            (base64) -> {device_id} (base64) ou {error}. É o endpoint que o nó chama.
//	selftest  — gera CA + attestation e verifica-a em-processo (prova a cadeia sem servidor).
//
// PRODUÇÃO: substituir a CA de dev pela raiz FIDO/organizacional e o AAGUID allowlist pela política
// real de dispositivos; o contrato (bytes-in/bytes-out, fail-closed) é o mesmo.
package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	attestation "github.com/aos-ref/platform/attestation"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "uso: aos-attestation <serve|selftest>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "selftest":
		err = cmdSelftest(os.Args[2:])
	default:
		err = fmt.Errorf("subcomando desconhecido %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "aos-attestation:", err)
		os.Exit(1)
	}
}

// wire request/response — espelho de integration/remote_attestation.go (bytes em base64 std).
type verifyRequest struct {
	AttestationObject string `json:"attestation_object"`
	ClientDataJSON    string `json:"client_data_json"`
	ExpectedChallenge string `json:"expected_challenge"`
}

type verifyResponse struct {
	DeviceID string `json:"device_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

func parseAAGUID(s string) ([16]byte, error) {
	var a [16]byte
	b, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	if err != nil || len(b) != 16 {
		return a, fmt.Errorf("AAGUID inválido (exige 32 hex): %q", s)
	}
	copy(a[:], b)
	return a, nil
}

func loadRoots(pemPath string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("nenhum certificado PEM em %q", pemPath)
	}
	return pool, nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8090", "endereço de escuta")
	rpID := fs.String("rpid", "aos.local", "Relying Party ID (ex.: aos.local)")
	origins := fs.String("origins", "https://aos.local", "origens web aceites, CSV (ex.: https://aos.local)")
	aaguidsCSV := fs.String("aaguids", "", "allowlist de AAGUID, CSV de hex de 32 chars (vazio em modo dev ⇒ um AAGUID de dev)")
	caPEM := fs.String("ca", "", "ficheiro PEM das âncoras de confiança (x5c); vazio ⇒ MODO DEV (CA auto-gerada + endpoint /synth)")
	tlsCert := fs.String("tls-cert", "", "cert PEM do servidor (TLS); presente ⇒ https (o nó exige https ou http-loopback)")
	tlsKey := fs.String("tls-key", "", "chave PEM do servidor (TLS)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var (
		roots   *x509.CertPool
		aaguids [][16]byte
		devMode = strings.TrimSpace(*caPEM) == ""
		devCAp  *devCA
	)
	// AAGUID de dev (o mesmo do selftest) quando não é dada allowlist.
	devAAGUID := [16]byte{0x9c, 0x83, 0x5a, 0x11, 0x1e, 0x4f, 0x42, 0x0a, 0xb1, 0x2d, 0x77, 0x30, 0x51, 0x0e, 0xa3, 0x01}
	if strings.TrimSpace(*aaguidsCSV) == "" {
		aaguids = [][16]byte{devAAGUID}
	} else {
		for _, h := range strings.Split(*aaguidsCSV, ",") {
			a, err := parseAAGUID(h)
			if err != nil {
				return err
			}
			aaguids = append(aaguids, a)
		}
	}
	if devMode {
		// MODO DEV: gera uma CA em memória e confia nela, e expõe /synth para produzir attestations
		// de teste amarradas a um challenge. Em PRODUÇÃO passa-se --ca (raiz FIDO/organizacional) e
		// /synth fica indisponível — só autenticadores REAIS certificados por essa raiz verificam.
		ca, err := newDevCA("AOS Dev Attestation Root")
		if err != nil {
			return err
		}
		devCAp = ca
		roots = x509.NewCertPool()
		roots.AddCert(ca.cert)
	} else {
		var err error
		if roots, err = loadRoots(*caPEM); err != nil {
			return err
		}
	}
	v, err := attestation.New(attestation.Config{
		RPID:           *rpID,
		AllowedOrigins: strings.Split(*origins, ","),
		AllowedAAGUIDs: aaguids,
		Roots:          roots,
	})
	if err != nil {
		return fmt.Errorf("construir verificador: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	if devMode {
		// Helper DEV: sintetiza uma attestation packed x5c válida amarrada ao challenge dado.
		mux.HandleFunc("POST /synth", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Challenge string `json:"challenge"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req)
			ch, err := base64.StdEncoding.DecodeString(req.Challenge)
			if err != nil || len(ch) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			att, cd, err := synthAttestation(devCAp, *rpID, strings.Split(*origins, ",")[0], devAAGUID, ch)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(verifyRequest{
				AttestationObject: base64.StdEncoding.EncodeToString(att),
				ClientDataJSON:    base64.StdEncoding.EncodeToString(cd),
				ExpectedChallenge: base64.StdEncoding.EncodeToString(ch),
			})
		})
	}
	mux.HandleFunc("POST /verify", func(w http.ResponseWriter, r *http.Request) {
		var req verifyRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			writeVerify(w, http.StatusBadRequest, verifyResponse{Error: "corpo inválido"})
			return
		}
		att, e1 := base64.StdEncoding.DecodeString(req.AttestationObject)
		cd, e2 := base64.StdEncoding.DecodeString(req.ClientDataJSON)
		ch, e3 := base64.StdEncoding.DecodeString(req.ExpectedChallenge)
		if e1 != nil || e2 != nil || e3 != nil {
			writeVerify(w, http.StatusBadRequest, verifyResponse{Error: "base64 inválido"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		deviceID, err := v.VerifyDeviceAttestation(ctx, att, cd, ch)
		if err != nil {
			// Recusa LEGÍTIMA: 200-diferente-de-OK não; devolve-se 422 com motivo saneado. O nó
			// converte qualquer não-200 em ErrDeviceAttestationRejected (fail-closed).
			writeVerify(w, http.StatusUnprocessableEntity, verifyResponse{Error: "attestation recusada"})
			return
		}
		writeVerify(w, http.StatusOK, verifyResponse{DeviceID: base64.StdEncoding.EncodeToString(deviceID)})
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	mode := "PRODUÇÃO (CA fornecida)"
	if devMode {
		mode = "DEV (CA auto-gerada + /synth)"
	}
	if strings.TrimSpace(*tlsCert) != "" && strings.TrimSpace(*tlsKey) != "" {
		fmt.Fprintf(os.Stderr, "[aos-attestation] a servir /verify em %s (HTTPS) — modo %s, rpid=%s, %d AAGUID allowlisted\n", *addr, mode, *rpID, len(aaguids))
		return srv.ListenAndServeTLS(*tlsCert, *tlsKey)
	}
	fmt.Fprintf(os.Stderr, "[aos-attestation] a servir /verify em %s (http claro; o nó exige https salvo loopback) — modo %s, rpid=%s, %d AAGUID allowlisted\n", *addr, mode, *rpID, len(aaguids))
	return srv.ListenAndServe()
}

func writeVerify(w http.ResponseWriter, code int, resp verifyResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}

// cmdSelftest prova a cadeia completa em-processo: gera uma CA, sintetiza uma attestation packed
// x5c amarrada a um challenge, e verifica-a com o attestation.Verifier ancorado nessa CA. Também
// prova o fail-closed: um challenge ERRADO é recusado.
func cmdSelftest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	rpID := fs.String("rpid", "aos.local", "Relying Party ID")
	origin := fs.String("origin", "https://aos.local", "origem web")
	if err := fs.Parse(args); err != nil {
		return err
	}
	aaguid := [16]byte{0x9c, 0x83, 0x5a, 0x11, 0x1e, 0x4f, 0x42, 0x0a, 0xb1, 0x2d, 0x77, 0x30, 0x51, 0x0e, 0xa3, 0x01}

	ca, err := newDevCA("AOS Dev Attestation Root")
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	v, err := attestation.New(attestation.Config{
		RPID: *rpID, AllowedOrigins: []string{*origin}, AllowedAAGUIDs: [][16]byte{aaguid}, Roots: roots,
	})
	if err != nil {
		return err
	}
	challenge := []byte("desafio-de-teste-por-perna-0001")
	att, cd, err := synthAttestation(ca, *rpID, *origin, aaguid, challenge)
	if err != nil {
		return err
	}
	// POSITIVO: attestation válida com o challenge certo -> deviceID não-vazio.
	deviceID, err := v.VerifyDeviceAttestation(context.Background(), att, cd, challenge)
	if err != nil {
		return fmt.Errorf("attestation válida devia verificar, veio: %w", err)
	}
	if len(deviceID) == 0 {
		return errors.New("deviceID vazio numa verificação OK")
	}
	// NEGATIVO: o MESMO material com um challenge diferente -> recusado (anti-replay do desafio).
	if _, err := v.VerifyDeviceAttestation(context.Background(), att, cd, []byte("challenge-errado")); err == nil {
		return errors.New("challenge errado devia ser RECUSADO (fail-closed)")
	}
	fmt.Printf("selftest OK: attestation packed x5c verificada; deviceID=%s (%d bytes); challenge errado recusado\n",
		hex.EncodeToString(deviceID)[:16], len(deviceID))
	return nil
}
