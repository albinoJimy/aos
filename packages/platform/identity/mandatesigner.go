package identity

// mandatesigner.go — O PINO DE QUEM ASSINA MANDATOS, NUM SÓ SÍTIO (AOS-446 fase 1).
//
// # O QUE ESTAVA DUPLICADO
//
// A gramática de `AOS_MANDATE_SIGNERS` (`user_id=chave,...`) tinha DOIS leitores, em módulos
// diferentes e sem nada que os obrigasse a concordar:
//
//   - `parseMandateSigners` no NÓ (`packages/cmd/aos/emissor_mandatado.go`), que impõe a forma
//     inteira — nomes repetidos, chaves partilhadas, prefixo `human:`;
//   - `pubkeyDoHumano` no EMISSOR (`packages/cmd/aos-issuer/mandato.go`), que percorre a MESMA
//     lista para tirar UMA chave e não impõe nada do resto.
//
// Enquanto a chave era sempre 64 hex a divergência era invisível. Deixa de ser no momento em que
// há DUAS formas de pino (hex cru e `sk-ssh-ed25519@openssh.com`): um leitor que só conheça a
// primeira lê a segunda como «chave inválida» — e o `pubkeyDoHumano`, que devolvia texto em vez
// de uma chave, nem sequer tinha onde a recusar. A regra de quem pode assinar um mandato não pode
// ter duas implementações.
//
// Passa a haver UMA: [ParseMandateSigners], aqui, no pacote onde o mandato vive. O nó e o emissor
// chamam-na, e o que um aceita o outro aceita.
//
// # AS DUAS FORMAS DE PINO
//
//	alice=6cc9… (64 hex)                                  chave ed25519 de SOFTWARE (a de sempre)
//	alice=sk-ssh-ed25519@openssh.com AAAAGnNr…            chave FIDO2, em HARDWARE (AOS-446)
//
// A forma do pino é que decide o que se aceita como assinatura — e não um campo do documento que
// chega com ele. Ver [SignedMandate.Format].

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Formas de assinatura de um mandato — o valor do campo `fmt` de [SignedMandate].
const (
	// MandateFormatEd25519 é a assinatura ed25519 CRUA sobre [Mandate.SigningInput]: a forma de
	// sempre. O campo `fmt` ausente significa exactamente isto, e é o que faz um mandato v1 ou v2
	// já assinado continuar a verificar sem lhe tocar.
	MandateFormatEd25519 = "ed25519"
	// MandateFormatSSHSIG é o envelope SSHSIG sobre uma chave FIDO2 `sk-ssh-ed25519@openssh.com`.
	MandateFormatSSHSIG = "sshsig"
	// MandateSSHSIGNamespace é a namespace SSHSIG dos mandatos — o segundo separador de domínio,
	// por cima do que o próprio [Mandate.SigningInput] já leva. Uma assinatura que o humano tenha
	// produzido para `git` ou `file` (as namespaces que o `ssh-keygen` usa por omissão noutros
	// usos) não verifica aqui, e vice-versa.
	MandateSSHSIGNamespace = "aos.identity.mandate"
	// MandateSSHSIGApplication é a `application` que uma chave FIDO2 TEM de ter para poder ser
	// pinada como assinante de mandatos (`ssh-keygen -t ed25519-sk -O application=…`). Entra
	// HASHADA no que o autenticador assina, pelo que é a segunda separação — a par da namespace —
	// entre uma assinatura de mandato e qualquer outra coisa que a mesma chave física faça.
	MandateSSHSIGApplication = "ssh:aos-mandate"
)

// mandateSignerRawDomain separa o cálculo da impressão digital de um pino de software de
// qualquer outro hash da mesma chave.
const mandateSignerRawDomain = "aos.identity.mandate.signer.ed25519"

// MandateSigner é a ÂNCORA de um humano que assina mandatos: ou uma chave ed25519 de software, ou
// uma chave FIDO2 residente num autenticador. É um valor opaco de propósito — constrói-se por
// [ParseMandateSigner] ou [RawMandateSigner], e nunca a partir de campos soltos, para que não
// exista um caminho que produza um pino meio-formado.
type MandateSigner struct {
	raw ed25519.PublicKey // != nil ⇒ pino de software
	sk  *SKPublicKey      // != nil ⇒ pino FIDO2
}

// RawMandateSigner pina uma chave ed25519 de software (a forma histórica).
func RawMandateSigner(pub ed25519.PublicKey) (MandateSigner, error) {
	if len(pub) != ed25519.PublicKeySize {
		return MandateSigner{}, fmt.Errorf("%w: pubkey ed25519 de %d bytes (esperados %d)", ErrMandateInvalid, len(pub), ed25519.PublicKeySize)
	}
	return MandateSigner{raw: append(ed25519.PublicKey(nil), pub...)}, nil
}

// SKMandateSigner pina uma chave FIDO2 já lida.
func SKMandateSigner(k SKPublicKey) (MandateSigner, error) {
	if len(k.Key) != ed25519.PublicKeySize || k.Application == "" || len(k.blob) == 0 {
		return MandateSigner{}, fmt.Errorf("%w: chave sk-ssh-ed25519 incompleta", ErrMandateInvalid)
	}
	c := k
	return MandateSigner{sk: &c}, nil
}

// ParseMandateSigner lê UM pino. `64 hex` ⇒ software; `sk-ssh-ed25519@openssh.com AAAA…` ⇒ FIDO2.
func ParseMandateSigner(texto string) (MandateSigner, error) {
	t := strings.TrimSpace(texto)
	if t == "" {
		return MandateSigner{}, fmt.Errorf("%w: pino vazio", ErrMandateInvalid)
	}
	if strings.HasPrefix(t, SSHSigAlgSKEd25519) {
		k, err := ParseSKPublicKey(t)
		if err != nil {
			return MandateSigner{}, fmt.Errorf("%w: %v", ErrMandateInvalid, err)
		}
		// A `application` TEM DE SER A DESTE USO (achado A5 da revisão adversarial). O parser
		// aceitava qualquer uma não-vazia, e o `ssh-keygen -t ed25519-sk` sem `-O application=`
		// produz `ssh:` — a chave de SSH interactiva do operador, que o §6.4 do ADR-033 manda
		// criar na MESMA máquina. Pinada como assinante de mandatos, cada login SSH passaria a
		// ser uma assinatura que o autenticador produz sob a mesma application, e o domínio
		// SSHSIG (namespace própria) seria a ÚNICA coisa a separar os dois usos. Exigir a
		// application põe uma segunda separação, e é a que o operador consegue ver no pino.
		if k.Application != MandateSSHSIGApplication {
			return MandateSigner{}, fmt.Errorf("%w: a chave FIDO2 tem application=%q e um pino de mandato exige %q — gere-a com `ssh-keygen -t ed25519-sk -O application=%s`",
				ErrMandateInvalid, k.Application, MandateSSHSIGApplication, MandateSSHSIGApplication)
		}
		return SKMandateSigner(k)
	}
	// UM PREFIXO `sk-` QUE NÃO SEJA O NOSSO ALGORITMO É RECUSA, e não uma tentativa de o ler como
	// hex: `sk-ecdsa-sha2-nistp256@openssh.com` é uma chave FIDO2 legítima que este código NÃO
	// verifica, e cair no ramo do hex daria a mensagem errada («nao e hex») a quem tem uma chave
	// de hardware do tipo errado.
	if strings.HasPrefix(t, "sk-") || strings.HasPrefix(t, "ssh-") || strings.HasPrefix(t, "ecdsa-") {
		return MandateSigner{}, fmt.Errorf("%w: %v", ErrMandateInvalid, fmt.Errorf("%w: pino %q", ErrSSHSigAlgo, strings.Fields(t)[0]))
	}
	b, err := hex.DecodeString(t)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return MandateSigner{}, fmt.Errorf("%w: pino nao e uma pubkey ed25519 em hex (64 caracteres) nem uma chave %s",
			ErrMandateInvalid, SSHSigAlgSKEd25519)
	}
	return RawMandateSigner(ed25519.PublicKey(b))
}

// MandatePinosMaximoPorHumano é o tecto de pinos que um humano pode ter em vigor ao mesmo tempo
// (AOS-446 fase 1, revisão de 2026-09-27).
//
// DOIS, e não «vários»: a lista com mais do que um pino existe para UMA rotação — a chave velha e
// a nova a coexistirem enquanto o humano re-assina o mandato. Três pinos não é uma rotação, é um
// conjunto de chaves que ninguém sabe justificar, e cada pino a mais é uma chave a mais que
// assina mandatos.
const MandatePinosMaximoPorHumano = 2

// ParseMandateSigners lê a lista inteira, no formato de `AOS_MANDATE_SIGNERS`:
// `user_id=pino,user_id=pino,...`.
//
// UM HUMANO PODE TER MAIS DO QUE UM PINO (AOS-446 fase 1, revisão de 2026-09-27). Antes era erro,
// e essa recusa tinha um custo que só se viu a desenhar o procedimento: trocar o pino de software
// pelo FIDO2 invalida NO MESMO INSTANTE todos os mandatos daquele humano — a verificação da
// assinatura corre antes de tudo o resto —, pelo que entre reescrever o `.env` e entregar o
// mandato novo a drenagem PARA. O dono escolheu a janela em vez da paragem programada
// (2026-09-27). Quem decide se a janela está ABERTA é o verificador, contra o relógio dele
// ([WithMandateDualPinUntil]) — aqui só se lê a forma.
//
// Recusa, fail-closed: entrada sem `=`, user_id vazio ou com o prefixo `human:` (o pino usa o
// user_id do token), mais do que [MandatePinosMaximoPorHumano] pinos para o mesmo humano, o MESMO
// pino repetido para o mesmo humano (não é uma rotação, é ruído), e CHAVE repetida entre dois
// nomes — a mesma chave sob dois nomes destrói a atribuição, porque um mandato de bob passaria a
// ser aceite como sendo de alice. Uma lista sem nenhuma entrada válida é um erro, e não um
// conjunto vazio: um conjunto vazio silencioso é um emissor mandatado sem ninguém que o limite.
//
// A vírgula separa entradas e NÃO aparece em base64 nem em hex, pelo que uma chave FIDO2 (que tem
// espaços) atravessa a lista sem escape. O `=` do padding de base64 também não estorva: a divisão
// é pelo PRIMEIRO `=`.
func ParseMandateSigners(lista string) (map[string][]MandateSigner, error) {
	out := make(map[string][]MandateSigner)
	porImpressao := make(map[string]string)
	porChaveCrua := make(map[string]string)
	for _, par := range strings.Split(lista, ",") {
		if strings.TrimSpace(par) == "" {
			continue
		}
		kv := strings.SplitN(par, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado user_id=pino)", ErrMandateInvalid, strings.TrimSpace(par))
		}
		humano, pino := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if humano == "" || strings.HasPrefix(humano, "human:") {
			return nil, fmt.Errorf("%w: user_id %q vazio ou com o prefixo human: (usa o user_id do token)", ErrMandateInvalid, humano)
		}
		s, err := ParseMandateSigner(pino)
		if err != nil {
			return nil, fmt.Errorf("%w (humano %q)", err, humano)
		}
		fp := s.Fingerprint()
		// A MESMA CHAVE SOB DOIS NOMES continua a ser recusada, e agora é a única forma de
		// repetição que o é: a repetição sob o MESMO nome é a janela de rotação.
		if outro, dup := porImpressao[fp]; dup {
			if outro == humano {
				return nil, fmt.Errorf("%w: o humano %q tem o MESMO pino duas vezes (%s) — uma rotacao sao duas chaves DIFERENTES", ErrMandateInvalid, humano, fp)
			}
			return nil, fmt.Errorf("%w: %q e %q partilham a mesma chave (%s)", ErrMandateInvalid, outro, humano, fp)
		}
		// E PELA CHAVE CRUA, que é o que a impressão não apanha ENTRE FORMAS (achado B3): a mesma
		// chave ed25519 pinada em hex para um humano e dentro de um blob FIDO2 para outro dá duas
		// impressões distintas e uma só chave privada. Sob o MESMO humano passa — é a forma que a
		// rotação pode tomar, e o detentor da chave é o mesmo em ambas as entradas.
		crua := hex.EncodeToString(s.chaveCrua())
		if outro, dup := porChaveCrua[crua]; dup && outro != humano {
			return nil, fmt.Errorf("%w: %q e %q partilham a MESMA chave ed25519 em formas diferentes (uma em hex, outra dentro de um blob %s) — quem a detiver assina mandatos por ambos",
				ErrMandateInvalid, outro, humano, SSHSigAlgSKEd25519)
		}
		porChaveCrua[crua] = humano
		porImpressao[fp] = humano
		if len(out[humano]) >= MandatePinosMaximoPorHumano {
			return nil, fmt.Errorf("%w: o humano %q tem mais de %d pinos — a janela de rotacao serve UMA troca", ErrMandateInvalid, humano, MandatePinosMaximoPorHumano)
		}
		out[humano] = append(out[humano], s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: lista de assinantes sem nenhuma entrada valida", ErrMandateInvalid)
	}
	return out, nil
}

// HumanosComDoisPinos devolve, ordenados, os humanos com mais do que um pino em vigor. É o que a
// fronteira de ambiente e o banner usam para decidir e declarar a janela de rotação.
func HumanosComDoisPinos(signers map[string][]MandateSigner) []string {
	var out []string
	for h, ps := range signers {
		if len(ps) > 1 {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// Valid diz se o pino está formado.
func (s MandateSigner) Valid() bool {
	return len(s.raw) == ed25519.PublicKeySize || (s.sk != nil && len(s.sk.Key) == ed25519.PublicKeySize)
}

// Hardware diz se o pino é uma chave FIDO2. É o que separa «o humano tocou numa chave que não se
// copia» de «alguém tinha um ficheiro».
func (s MandateSigner) Hardware() bool { return s.sk != nil }

// Kind devolve a forma do pino, no vocabulário do campo `fmt`: [MandateFormatEd25519] ou
// [MandateFormatSSHSIG]. Um pino não formado devolve "".
func (s MandateSigner) Kind() string {
	switch {
	case s.sk != nil:
		return MandateFormatSSHSIG
	case len(s.raw) == ed25519.PublicKeySize:
		return MandateFormatEd25519
	default:
		return ""
	}
}

// RawKey devolve a chave ed25519 de um pino de software (nil num pino FIDO2). Existe para as
// verificações de colisão do composition-root, que comparam pinos com a chave do emissor.
func (s MandateSigner) RawKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), s.raw...)
}

// chaveCrua devolve os bytes da chave ed25519 SUBJACENTE, seja qual for a forma do pino — a chave
// solta num pino de software, a chave de dentro do blob num pino FIDO2.
//
// EXISTE POR CAUSA DE UM BURACO REAL (achado B3 da 2.ª ronda de revisão de segurança). A guarda
// de «a mesma chave sob dois nomes» indexava por [MandateSigner.Fingerprint], que é
// `ed25519:<hash>` num pino de software e `SHA256:<b64>` num FIDO2 — dois valores diferentes para
// a MESMA chave ed25519. Medido: `alice=<hex K>,bob=<sk-ssh-ed25519 com K>` era ACEITE, e quem
// detivesse K assinava mandatos em nome da alice E do bob. É exactamente o que a guarda existe
// para recusar, e a impressão sozinha não a impunha.
func (s MandateSigner) chaveCrua() []byte {
	switch {
	case s.sk != nil:
		return s.sk.Key
	default:
		return s.raw
	}
}

// Fingerprint é a IMPRESSÃO DIGITAL do pino — o que entra no selo de cada decisão (o WORM v5) e
// no registo das âncoras no arranque.
//
// Num pino FIDO2 é EXACTAMENTE a que o `ssh-keygen -lf chave.pub` imprime (`SHA256:<base64>`), de
// propósito: o operador confere o que o WORM selou contra o que a ferramenta dele mostra, sem
// converter nada. Num pino de software é `ed25519:<32 hex>` de um SHA-256 com domínio próprio —
// prefixo distinto, e por isso as duas famílias nunca se confundem nem colidem.
//
// Um pino não formado devolve "" — nunca uma impressão que pareça válida.
func (s MandateSigner) Fingerprint() string {
	switch {
	case s.sk != nil:
		return s.sk.SSHFingerprint()
	case len(s.raw) == ed25519.PublicKeySize:
		sum := sha256.Sum256(append([]byte(mandateSignerRawDomain), s.raw...))
		return "ed25519:" + hex.EncodeToString(sum[:])[:32]
	default:
		return ""
	}
}

// String é a forma legível do pino, para banners e diagnósticos. Nunca imprime material secreto
// (um pino é público por definição), mas também não despeja a chave inteira: a impressão chega.
func (s MandateSigner) String() string {
	if s.sk != nil {
		return fmt.Sprintf("FIDO2 %s application=%q", s.sk.SSHFingerprint(), s.sk.Application)
	}
	if len(s.raw) == ed25519.PublicKeySize {
		return "software " + s.Fingerprint()
	}
	return "pino invalido"
}

// Verify verifica `sig` sobre `mensagem`, na forma `formato`, contra este pino.
//
// A TABELA, e é a decisão que o AOS-446 fase 1 tomou (ADR-033 §8):
//
//	pino        fmt ausente / "ed25519"     fmt "sshsig"
//	software    aceita (a forma de sempre)  RECUSA — o pino nao e hardware
//	FIDO2       RECUSA — ver abaixo         aceita (SSHSIG, com presenca de utilizador)
//
// As duas recusas são o que torna o campo `fmt` INOFENSIVO: quem o troque no documento não muda
// nada do que é aceite, porque quem decide é o pino — que vive no `.env` do nó e não viaja com o
// mandato. Em particular, um pino FIDO2 NÃO aceita uma assinatura ed25519 crua: se aceitasse,
// bastaria ao atacante deitar mão à chave privada correspondente (que, num autenticador, não
// existe fora dele — mas o argumento não pode depender disso) para assinar sem toque nenhum, e o
// caminho de hardware não provaria nada.
func (s MandateSigner) Verify(formato string, sig, mensagem []byte) error {
	// O `fmt` NÃO SE APARA. Aparava-se, e `" sshsig "` passava — achado A5 da revisão
	// adversarial. O campo é uma enumeração fechada de dois valores escritos por uma
	// ferramenta, não texto de um humano: aceitar variantes com espaço é tolerância que só
	// serve para haver duas escritas do mesmo documento que não são a mesma string.
	f := formato
	if f == "" {
		f = MandateFormatEd25519
	}
	if f != MandateFormatEd25519 && f != MandateFormatSSHSIG {
		return fmt.Errorf("%w: fmt=%q nao e um formato conhecido (%q ou %q, sem espacos)",
			ErrMandateInvalid, formato, MandateFormatEd25519, MandateFormatSSHSIG)
	}
	switch {
	case s.sk != nil:
		if f != MandateFormatSSHSIG {
			return fmt.Errorf("%w: o humano esta pinado com uma chave FIDO2 (%s) e o mandato diz fmt=%q — um pino de hardware so aceita %q",
				ErrMandateInvalid, s.sk.SSHFingerprint(), f, MandateFormatSSHSIG)
		}
		if _, err := VerifySSHSIG(sig, *s.sk, MandateSSHSIGNamespace, mensagem); err != nil {
			return fmt.Errorf("%w: %v", ErrMandateInvalid, err)
		}
		return nil
	case len(s.raw) == ed25519.PublicKeySize:
		if f != MandateFormatEd25519 {
			return fmt.Errorf("%w: o mandato diz fmt=%q e o humano esta pinado com uma chave de SOFTWARE — troque o pino para %s antes de assinar com FIDO2",
				ErrMandateInvalid, f, SSHSigAlgSKEd25519)
		}
		if len(sig) != ed25519.SignatureSize {
			return fmt.Errorf("%w: assinatura de %d bytes (esperados %d)", ErrMandateInvalid, len(sig), ed25519.SignatureSize)
		}
		if !ed25519.Verify(s.raw, mensagem, sig) {
			return fmt.Errorf("%w: assinatura nao verifica com a chave pinada", ErrMandateInvalid)
		}
		return nil
	default:
		return fmt.Errorf("%w: chave do signatario invalida", ErrMandateInvalid)
	}
}

// MandateSignersDigest resume um conjunto de pinos numa só linha determinista, para o registo das
// âncoras de confiança no WORM (AOS-446 fase 1). A ordem dos NOMES é fixada por ordenação — um
// mapa não tem ordem, e um digest que dependesse dela mudaria de arranque para arranque e
// denunciaria trocas que não houve. A ordem dos PINOS de cada humano também se ordena, pela mesma
// razão e mais uma: reordenar as duas entradas de uma rotação no `.env` não é uma troca de
// autoridade, e não deve fazer o selador recusar.
func MandateSignersDigest(signers map[string][]MandateSigner) string {
	nomes := make([]string, 0, len(signers))
	for n := range signers {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	h := sha256.New()
	for _, n := range nomes {
		fps := make([]string, 0, len(signers[n]))
		for _, s := range signers[n] {
			fps = append(fps, s.Fingerprint())
		}
		sort.Strings(fps)
		fmt.Fprintf(h, "%d:%s,%d:", len(n), n, len(fps))
		for _, fp := range fps {
			fmt.Fprintf(h, "%d:%s,", len(fp), fp)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
