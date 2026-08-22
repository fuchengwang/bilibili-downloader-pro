package bilibili

import (
	"context"
	"testing"
)

func TestParseInput(t *testing.T) {
	client := GetDefaultClient()
	ctx := context.Background()

	tests := []struct {
		input       string
		expectedTyp TargetType
		expectedBV  string
		expectedAID string
		expectedEP  string
		expectedSS  string
		expectedP   int
	}{
		{
			input:       "https://www.bilibili.com/video/BV1xx411c7mD",
			expectedTyp: TargetNormal,
			expectedBV:  "BV1xx411c7mD",
			expectedP:   1,
		},
		{
			input:       "https://www.bilibili.com/video/BV1xx411c7mD?p=3",
			expectedTyp: TargetNormal,
			expectedBV:  "BV1xx411c7mD",
			expectedP:   3,
		},
		{
			input:       "【好视频推荐】https://www.bilibili.com/video/av170001 赶紧来看！",
			expectedTyp: TargetNormal,
			expectedAID: "170001",
			expectedP:   1,
		},
		{
			input:       "https://www.bilibili.com/bangumi/play/ep123456",
			expectedTyp: TargetBangumi,
			expectedEP:  "123456",
			expectedP:   1,
		},
		{
			input:       "https://www.bilibili.com/bangumi/play/ss654321",
			expectedTyp: TargetBangumi,
			expectedSS:  "654321",
			expectedP:   1,
		},
		{
			input:       "BV1u24y1B7hT",
			expectedTyp: TargetNormal,
			expectedBV:  "BV1u24y1B7hT",
			expectedP:   1,
		},
	}

	for _, tc := range tests {
		res, err := client.ParseInput(ctx, tc.input)
		if err != nil {
			t.Fatalf("Failed to parse %s: %v", tc.input, err)
		}
		if res.Type != tc.expectedTyp {
			t.Errorf("For %s, expected type %s, got %s", tc.input, tc.expectedTyp, res.Type)
		}
		if tc.expectedBV != "" && res.BVID != tc.expectedBV {
			t.Errorf("For %s, expected BV %s, got %s", tc.input, tc.expectedBV, res.BVID)
		}
		if tc.expectedAID != "" && res.AID != tc.expectedAID {
			t.Errorf("For %s, expected AID %s, got %s", tc.input, tc.expectedAID, res.AID)
		}
		if tc.expectedEP != "" && res.EPID != tc.expectedEP {
			t.Errorf("For %s, expected EP %s, got %s", tc.input, tc.expectedEP, res.EPID)
		}
		if tc.expectedSS != "" && res.SSID != tc.expectedSS {
			t.Errorf("For %s, expected SS %s, got %s", tc.input, tc.expectedSS, res.SSID)
		}
		if res.Page != tc.expectedP {
			t.Errorf("For %s, expected Page %d, got %d", tc.input, tc.expectedP, res.Page)
		}
	}
}
