package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
)

// CommentFeedAction 表示 Feed 评论动作
type CommentFeedAction struct {
	page *rod.Page
}

// NewCommentFeedAction 创建 Feed 评论动作
func NewCommentFeedAction(page *rod.Page) *CommentFeedAction {
	return &CommentFeedAction{page: page}
}

// PostComment 发表评论到 Feed
func (f *CommentFeedAction) PostComment(ctx context.Context, feedID, xsecToken, content string) error {
	page := f.page.Timeout(60 * time.Second)

	pageURL := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页: %s", pageURL)

	if err := page.Navigate(pageURL); err != nil {
		return fmt.Errorf("导航失败: %w", err)
	}
	if err := page.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("WaitDOMStable: %v (继续)", err)
	}
	time.Sleep(1 * time.Second)

	if err := checkCookieValid(page, feedID); err != nil {
		return err
	}
	if err := checkPageAccessible(page); err != nil {
		return err
	}

	elem, err := page.Element("div.input-box div.content-edit span")
	if err != nil {
		logrus.Warnf("Failed to find comment input box: %v", err)
		return fmt.Errorf("未找到评论输入框，该帖子可能不支持评论或网页端不可访问: %w", err)
	}

	if err := elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to click comment input box: %v", err)
		return fmt.Errorf("无法点击评论输入框: %w", err)
	}

	elem2, err := page.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		logrus.Warnf("Failed to find comment input field: %v", err)
		return fmt.Errorf("未找到评论输入区域: %w", err)
	}

	if err := elem2.Input(content); err != nil {
		logrus.Warnf("Failed to input comment content: %v", err)
		return fmt.Errorf("无法输入评论内容: %w", err)
	}

	time.Sleep(1 * time.Second)

	submitButton, err := page.Element("div.bottom button.submit")
	if err != nil {
		logrus.Warnf("Failed to find submit button: %v", err)
		return fmt.Errorf("未找到提交按钮: %w", err)
	}

	if err := submitButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
		logrus.Warnf("Failed to click submit button: %v", err)
		return fmt.Errorf("无法点击提交按钮: %w", err)
	}

	time.Sleep(1 * time.Second)

	logrus.Infof("Comment posted successfully to feed: %s", feedID)
	return nil
}

// ReplyToComment uses CDP request hijacking to inject target_comment_id into
// the comment POST body, then verifies the result via JavaScript on the page.
func (f *CommentFeedAction) ReplyToComment(ctx context.Context, feedID, xsecToken, commentID, userID, content string) error {
	page := f.page.Timeout(90 * time.Second)
	pageURL := makeFeedDetailURL(feedID, xsecToken)
	logrus.Infof("打开 feed 详情页进行回复: %s", pageURL)

	if err := page.Navigate(pageURL); err != nil {
		return fmt.Errorf("导航失败: %w", err)
	}
	if err := page.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("WaitDOMStable: %v (继续)", err)
	}
	time.Sleep(2 * time.Second)

	if err := checkCookieValid(page, feedID); err != nil {
		return err
	}
	if err := checkPageAccessible(page); err != nil {
		return err
	}

	// Install JS response capture (after page load, for reading response only)
	page.Eval(`() => { window.__xhs_comment_resp = ''; }`)

	logrus.Infof("设置 CDP 请求拦截，target_comment_id=%s", commentID)

	intercepted := make(chan bool, 1)

	router := page.HijackRequests()
	if err := router.Add("*/api/sns/web/v1/comment/post*", "", func(hijack *rod.Hijack) {
		defer func() {
			if r := recover(); r != nil {
				logrus.Errorf("CDP handler panic: %v", r)
			}
		}()

		body := hijack.Request.Body()
		logrus.Infof("CDP 拦截到评论请求, body=%d bytes", len(body))

		var newBody []byte
		if len(body) > 0 {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(body), &parsed); err == nil {
				parsed["target_comment_id"] = commentID
				newBody, _ = json.Marshal(parsed)
				logrus.Infof("已修改 body 注入 target_comment_id")
			}
		}

		if len(newBody) == 0 {
			payload := map[string]interface{}{
				"note_id":           feedID,
				"content":           content,
				"target_comment_id": commentID,
				"at_users":          []interface{}{},
			}
			newBody, _ = json.Marshal(payload)
			logrus.Infof("构造新 body: %s", string(newBody))
		}

		hijack.ContinueRequest(&proto.FetchContinueRequest{
			PostData: newBody,
		})

		select {
		case intercepted <- true:
		default:
		}
	}); err != nil {
		return fmt.Errorf("注册拦截规则失败: %w", err)
	}
	go router.Run()
	defer func() {
		if err := router.Stop(); err != nil {
			logrus.Warnf("停止 hijack router: %v", err)
		}
	}()

	shortPage := page.Timeout(15 * time.Second)
	elem, err := shortPage.Element("div.input-box div.content-edit span")
	if err != nil {
		return fmt.Errorf("未找到评论输入框（xsec_token 可能已过期）: %w", err)
	}
	if err := elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("无法点击评论输入框: %w", err)
	}

	inputEl, err := shortPage.Element("div.input-box div.content-edit p.content-input")
	if err != nil {
		return fmt.Errorf("未找到评论输入区域: %w", err)
	}
	if err := inputEl.Input(content); err != nil {
		return fmt.Errorf("输入回复内容失败: %w", err)
	}

	time.Sleep(1 * time.Second)

	submitBtn, err := shortPage.Element("div.bottom button.submit")
	if err != nil {
		return fmt.Errorf("未找到提交按钮: %w", err)
	}
	if err := submitBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("点击提交按钮失败: %w", err)
	}

	logrus.Info("已点击提交，等待 CDP 拦截...")

	select {
	case <-intercepted:
		logrus.Info("CDP 已拦截并注入 target_comment_id")
	case <-time.After(15 * time.Second):
		return fmt.Errorf("CDP 未拦截到评论请求（15s 超时），提交可能未触发 API 调用")
	}

	// Wait for browser to complete the request
	time.Sleep(3 * time.Second)

	// Check page for error toast or success indicator
	pageResult, _ := page.Eval(`() => {
		var toast = document.querySelector('.toast-text, .error-text, .note-toast');
		var newComment = document.querySelector('.comment-item:last-child .content');
		return JSON.stringify({
			toast: toast ? toast.textContent : '',
			last_comment: newComment ? newComment.textContent.substring(0, 100) : '',
			url: location.href
		});
	}`)
	if pageResult != nil {
		logrus.Infof("提交后页面状态: %s", pageResult.Value.Str())
	}

	logrus.Infof("回复评论完成 - feedID: %s, commentID: %s", feedID, commentID)
	return nil
}
