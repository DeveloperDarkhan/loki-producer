package validation

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/prometheus/common/model"

	"github.com/your-org/simple-distributor/internal/config"
	"github.com/your-org/simple-distributor/internal/model"
)

var (
	labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

type Validator struct {
	lim config.Limits

	rejectOldMaxAge    time.Duration
	creationGrace      time.Duration
}

func New(lim config.Limits) *Validator {
	oldDur, _ := time.ParseDuration(lim.RejectOldSamplesMaxAge)
	grace, _ := time.ParseDuration(lim.CreationGracePeriod)
	return &Validator{
		lim:             lim,
		rejectOldMaxAge: oldDur,
		creationGrace:   grace,
	}
}

type ValidatedEntry struct {
	Tenant    string
	LabelsStr string
	Timestamp time.Time
	Line      string
}

func (v *Validator) ValidatePush(tenant string, req *model.PushRequest) ([]ValidatedEntry, error) {
	var out []ValidatedEntry
	now := time.Now()

	for _, s := range req.Streams {
		// 1. Validate/normalize labels
		if len(s.Stream) == 0 {
			return nil, errors.New("empty stream labels")
		}
		if len(s.Stream) > v.lim.MaxLabelNamesPerSeries {
			return nil, fmt.Errorf("too many labels: %d > %d", len(s.Stream), v.lim.MaxLabelNamesPerSeries)
		}
		type kv struct{ k, v string }
		kvList := make([]kv, 0, len(s.Stream))
		for k, val := range s.Stream {
			if !labelNameRE.MatchString(k) {
				return nil, fmt.Errorf("invalid label name %q", k)
			}
			if len(k) > v.lim.MaxLabelNameLength {
				return nil, fmt.Errorf("label name too long %q", k)
			}
			if len(val) > v.lim.MaxLabelValueLength {
				return nil, fmt.Errorf("label value too long for %q", k)
			}
			kvList = append(kvList, kv{k, val})
		}
		sort.Slice(kvList, func(i, j int) bool { return kvList[i].k < kvList[j].k })
		labelsStr := "{"
		for i, p := range kvList {
			if i > 0 {
				labelsStr += ","
			}
			labelsStr += p.k + "=\"" + p.v + "\""
		}
		labelsStr += "}"

		// 2. Entries
		for _, pair := range s.Values {
			if len(pair) != 2 {
				return nil, fmt.Errorf("bad value tuple length")
			}
			nsStr := pair[0]
			line := pair[1]

			if v.lim.MaxLineSize > 0 && len(line) > v.lim.MaxLineSize {
				if v.lim.MaxLineSizeTruncate {
					if len(line) > v.lim.MaxLineSize-len(v.lim.MaxLineSizeTruncateIdent) {
						line = line[:v.lim.MaxLineSize-len(v.lim.MaxLineSizeTruncateIdent)] + v.lim.MaxLineSizeTruncateIdent
					}
				} else {
					return nil, fmt.Errorf("line too long (%d > %d)", len(line), v.lim.MaxLineSize)
				}
			}

			// Loki формат timestamp в наносекундах строкой.
			ns, err := strconv.ParseInt(nsStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid timestamp ns=%q: %w", nsStr, err)
			}
			ts := time.Unix(0, ns)

			if v.lim.RejectOldSamples && v.rejectOldMaxAge > 0 {
				if now.Sub(ts) > v.rejectOldMaxAge {
					return nil, fmt.Errorf("entry too old (ts=%s age=%s > max=%s)", ts, now.Sub(ts), v.rejectOldMaxAge)
				}
			}
			if v.creationGrace > 0 {
				if ts.After(now.Add(v.creationGrace)) {
					return nil, fmt.Errorf("entry too far in future (ts=%s now=%s grace=%s)", ts, now, v.creationGrace)
				}
			}

			out = append(out, ValidatedEntry{
				Tenant:    tenant,
				LabelsStr: labelsStr,
				Timestamp: ts,
				Line:      line,
			})
		}
	}
	return out, nil
}

// (Простая модель совместимая с internal/model, но вынесена отдельно,
// чтобы не зависеть от protobuf.)
type PushRequestAlias = model.PushRequest
