package bench

import (
	"bench/counter"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/PuerkitoBio/goquery"
)

func checkHTML(f func(*http.Response, *goquery.Document) error) func(*http.Response, *bytes.Buffer) error {
	return func(res *http.Response, body *bytes.Buffer) error {
		doc, err := goquery.NewDocumentFromReader(body)
		if err != nil {
			return fatalErrorf("ページのHTMLがパースできませんでした")
		}
		return f(res, doc)
	}
}

func checkRedirectStatusCode(res *http.Response, body *bytes.Buffer) error {
	if res.StatusCode == 302 || res.StatusCode == 303 {
		return nil
	}
	return fmt.Errorf("期待していないステータスコード %d Expected 302 or 303", res.StatusCode)
}

func loadStaticFile(ctx context.Context, checker *Checker, path string) error {
	return checker.Play(ctx, &CheckAction{
		EnableCache:          true,
		SkipIfCacheAvailable: true,

		Method: "GET",
		Path:   path,
		CheckFunc: func(res *http.Response, body *bytes.Buffer) error {
			// Note. EnableCache時はPlay時に自動でReponseは最後まで読まれる
			if res.StatusCode == http.StatusOK {
				counter.IncKey("staticfile-200")
			} else if res.StatusCode == http.StatusNotModified {
				counter.IncKey("staticfile-304")
			} else {
				return fmt.Errorf("期待していないステータスコード %d", res.StatusCode)
			}
			return nil
		},
	})
}

func goLoadStaticFiles(ctx context.Context, checker *Checker, paths ...string) {
	for _, path := range paths {
		go loadStaticFile(ctx, checker, path)
	}
}

func goLoadAsset(ctx context.Context, checker *Checker) {
	var assetFiles []string
	for _, sf := range StaticFiles {
		assetFiles = append(assetFiles, sf.Path)
	}
	goLoadStaticFiles(ctx, checker, assetFiles...)
}

func LoadCreateUser(ctx context.Context, state *State) error {
	user, checker, newUserPush := state.PopNewUser()
	if user == nil {
		return nil
	}

	err := checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/users",
		ExpectedStatusCode: 201,
		PostData: map[string]string{
			"nickname":   user.Nickname,
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "新規ユーザが作成できること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 200,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "作成したユーザでログインできること",
	})
	if err != nil {
		return err
	}

	newUserPush()

	return nil
}

func LoadLogin(ctx context.Context, state *State) error {
	user, checker, push := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer push()

	act := &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 200,
		Description:        "ログインできること",
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
	}

	err := checker.Play(ctx, act)
	if err != nil {
		return err
	}

	act = &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/logout",
		ExpectedStatusCode: 204,
		Description:        "ログアウトできること",
	}

	err = checker.Play(ctx, act)
	if err != nil {
		return err
	}

	return nil
}

// イベントが公開されるのを待ってトップページをF5連打するユーザがいる
// イベント一覧はログインしていてもしていなくても取れる
func LoadTopPage(ctx context.Context, state *State) error {
	user, checker, push := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer push()
	// checker.ResetCookie()  // may already login, or not

	events := []JsonEvent{}

	err := checker.Play(ctx, &CheckAction{
		Method:             "GET",
		Path:               "/",
		ExpectedStatusCode: 200,
		Description:        "ページが表示されること",
		CheckFunc: checkHTML(func(res *http.Response, doc *goquery.Document) error {
			selection := doc.Find("#app-wrapper")
			if selection == nil || len(selection.Nodes) == 0 {
				return fatalErrorf("app-wrapperが見つかりません")
			}

			node := selection.Nodes[0]
			for _, attr := range node.Attr {
				if attr.Key == "data-events" {
					err := json.Unmarshal([]byte(attr.Val), &events)
					// TODO(sonots): Validate number of remains, total of events?
					// TODO(sonots): Validate number of remains, total of ranked sheets of events?
					if err != nil {
						return fatalErrorf("イベント一覧のJsonデコードに失敗 %v", err)
					}
					return nil
				}
			}

			return fatalErrorf("app-wrapperにdata-eventsがありません")
		}),
	})
	if err != nil {
		return err
	}
	return nil
}

// 席は(rank 内で)ランダムに割り当てられるため、良い席に当たるまで予約連打して、キャンセルするユーザがいる
func LoadReserveSheet(ctx context.Context, state *State) error {
	return nil
}

// Validation

func CheckStaticFiles(ctx context.Context, state *State) error {
	user, checker, push := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer push()

	for _, staticFile := range StaticFiles {
		sf := staticFile
		err := checker.Play(ctx, &CheckAction{
			Method:             "GET",
			Path:               sf.Path,
			ExpectedStatusCode: 200,
			Description:        "静的ファイルが取得できること",
			CheckFunc: func(res *http.Response, body *bytes.Buffer) error {
				hasher := md5.New()
				_, err := io.Copy(hasher, body)
				if err != nil {
					return fatalErrorf("レスポンスボディの取得に失敗 %v", err)
				}
				hash := hex.EncodeToString(hasher.Sum(nil))
				if hash != sf.Hash {
					return fatalErrorf("静的ファイルの内容が正しくありません")
				}
				return nil
			},
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func CheckCreateUser(ctx context.Context, state *State) error {
	user, checker, push := state.PopNewUser()
	if user == nil {
		return nil
	}

	err := checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/users",
		ExpectedStatusCode: 201,
		PostData: map[string]string{
			"nickname":   user.Nickname,
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "新規ユーザが作成できること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 200,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "作成したユーザでログインできること",
	})
	if err != nil {
		return err
	}

	defer push()

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/logout",
		ExpectedStatusCode: 204,
		Description:        "ログアウトできること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/users",
		ExpectedStatusCode: 409,
		PostData: map[string]string{
			"nickname":   user.Nickname,
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "すでに作成済みの場合エラーになること",
	})
	if err != nil {
		return err
	}

	return nil
}

func CheckLogin(ctx context.Context, state *State) error {
	user, checker, push := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer push()

	err := checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 200,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "ログインできること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/logout",
		ExpectedStatusCode: 204,
		Description:        "ログアウトできること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/logout",
		ExpectedStatusCode: 401,
		Description:        "ログアウト済みの場合エラーになること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 401,
		PostData: map[string]string{
			"login_name": RandomAlphabetString(32),
			"password":   user.Password,
		},
		Description: "存在しないユーザでログインできないこと",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 401,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   RandomAlphabetString(32),
		},
		Description: "パスワードが間違っている場合ログインできないこと",
	})
	if err != nil {
		return err
	}

	return nil
}

func CheckTopPage(ctx context.Context, state *State) error {
	// 1. ヘッダー部分の確認
	//   ログイン済みの場合ユーザー名が表示されていること
	//   ログインしていない場合ユーザー名が表示されていないこと
	// 2. DOM 構造が変わっていないこと
	// 3. イベント一覧が一定期間以内に更新されていること
	//   何秒許容するかは要検討
	// 4. イベント一覧の残席数が正しく更新されていること（要検討）
	return nil
}

func CheckReserveSheet(ctx context.Context, state *State) error {
	// 購入の成功
	// 売り切れの場合エラーになること
	// ログインしていない場合にエラーになること

	// キャンセルの成功
	// すでにキャンセル済みの場合エラーになること
	// 購入していないチケットをキャンセルしようとしたらエラーになること
	// ログインしていない場合エラーになること
	return nil
}

func CheckAdminLogin(ctx context.Context, state *State) error {
	admin, checker, push := state.PopRandomAdministrator()
	if admin == nil {
		return nil
	}
	defer push()

	user, userChecker, userPush := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer userPush()

	err := userChecker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/login",
		ExpectedStatusCode: 401,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "一般ユーザで管理者ログインできないこと",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/login",
		ExpectedStatusCode: 204,
		PostData: map[string]string{
			"login_name": admin.LoginName,
			"password":   admin.Password,
		},
		Description: "管理者でログインできること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/logout",
		ExpectedStatusCode: 204,
		Description:        "管理者でログアウトできること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/logout",
		ExpectedStatusCode: 401,
		Description:        "ログアウト済みの場合エラーになること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/login",
		ExpectedStatusCode: 401,
		PostData: map[string]string{
			"login_name": RandomAlphabetString(32),
			"password":   admin.Password,
		},
		Description: "存在しないユーザで管理者ログインできないこと",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/login",
		ExpectedStatusCode: 401,
		PostData: map[string]string{
			"login_name": admin.LoginName,
			"password":   RandomAlphabetString(32),
		},
		Description: "パスワードが間違っている場合管理者ログインできないこと",
	})
	if err != nil {
		return err
	}

	return nil
}

func CheckAdminCreateEvent(ctx context.Context, state *State) error {
	admin, checker, push := state.PopRandomAdministrator()
	if admin == nil {
		return nil
	}
	defer push()

	user, userChecker, userPush := state.PopRandomUser()
	if user == nil {
		return nil
	}
	defer userPush()

	// TODO(sonots): Skip login if already logged in?
	err := checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/actions/login",
		ExpectedStatusCode: 204,
		PostData: map[string]string{
			"login_name": admin.LoginName,
			"password":   admin.Password,
		},
		Description: "管理者でログインできること",
	})
	if err != nil {
		return err
	}

	// TODO(sonots): Skip login if already logged in?
	err = userChecker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/api/actions/login",
		ExpectedStatusCode: 200,
		PostData: map[string]string{
			"login_name": user.LoginName,
			"password":   user.Password,
		},
		Description: "一般ユーザでログインできること",
	})
	if err != nil {
		return err
	}

	event, newEventPush := state.PopNewEvent()
	if event == nil {
		return nil
	}

	jsonEvent := JsonEvent{}
	checkJsonEventResponse := func(res *http.Response, body *bytes.Buffer) error {
		dec := json.NewDecoder(body)
		err := dec.Decode(&jsonEvent)
		if err != nil {
			return fatalErrorf("Jsonのデコードに失敗 %v", err)
		}
		if jsonEvent.ID != event.ID || jsonEvent.Title != event.Title {
			return fatalErrorf("正しいイベントを取得できません")
		}
		return nil
	}

	err = userChecker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/events",
		ExpectedStatusCode: 403,
		PostData: map[string]string{
			"title":     event.Title,
			"public_fg": "", // false
			"price":     fmt.Sprint(event.Price),
		},
		Description: "一般ユーザがイベントを作成できないこと",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               "/admin/api/events",
		ExpectedStatusCode: 200,
		PostData: map[string]string{
			"title":     event.Title,
			"public_fg": "", // false
			"price":     fmt.Sprint(event.Price),
		},
		Description: "管理者がイベントを作成できること",
		CheckFunc:   checkJsonEventResponse,
	})
	if err != nil {
		return err
	}

	err = userChecker.Play(ctx, &CheckAction{
		Method:             "GET",
		Path:               fmt.Sprintf("/api/events/%d", event.ID),
		ExpectedStatusCode: 404,
		Description:        "一般ユーザが非公開イベントを取得できないこと",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "GET",
		Path:               fmt.Sprintf("/admin/api/events/%d", event.ID),
		ExpectedStatusCode: 200,
		Description:        "管理者がイベントを取得できること",
		CheckFunc:          checkJsonEventResponse,
	})
	if err != nil {
		return err
	}

	err = userChecker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               fmt.Sprintf("/admin/api/events/%d/actions/edit", event.ID),
		ExpectedStatusCode: 403,
		Description:        "一般ユーザがイベントを編集できないこと",
		PostData: map[string]string{
			"title":  event.Title,
			"public": "true",
			"price":  fmt.Sprint(event.Price),
		},
	})
	if err != nil {
		return err
	}

	event.Title = RandomAlphabetString(32)
	event.PublicFg = true
	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               fmt.Sprintf("/admin/api/events/%d/actions/edit", event.ID),
		ExpectedStatusCode: 200,
		Description:        "管理者がイベントを編集できること",
		PostData: map[string]string{
			"title":  event.Title,
			"public": "true",
			"price":  fmt.Sprint(event.Price),
		},
		CheckFunc: checkJsonEventResponse,
	})
	if err != nil {
		return err
	}

	err = userChecker.Play(ctx, &CheckAction{
		Method:             "GET",
		Path:               fmt.Sprintf("/api/events/%d", event.ID),
		ExpectedStatusCode: 404,
		Description:        "一般ユーザが公開イベントを取得できること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "GET",
		Path:               fmt.Sprintf("/admin/api/events/%d", event.ID+1),
		ExpectedStatusCode: 404,
		Description:        "イベントが存在しない場合取得に失敗すること",
	})
	if err != nil {
		return err
	}

	err = checker.Play(ctx, &CheckAction{
		Method:             "POST",
		Path:               fmt.Sprintf("/admin/api/events/%d/actions/edit", event.ID+1),
		ExpectedStatusCode: 404,
		Description:        "イベントが存在しない場合編集に失敗すること",
		PostData: map[string]string{
			"title":  event.Title,
			"public": "true",
			"price":  fmt.Sprint(event.Price),
		},
	})
	if err != nil {
		return err
	}

	newEventPush()

	return nil
}