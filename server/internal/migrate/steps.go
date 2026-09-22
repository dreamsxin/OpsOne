package migrate

// 迁移步骤清单。**只追加，不修改已发布的步骤**。
//
// 改一个已经在别人库里跑过的步骤是没有意义的：那边已经记成「已应用」，
// 改动永远不会再执行。要修就加一个新版本号。
//
// v1 / v2 这两步是把原来散在 `db.Migrate` 里的两段回填搬过来的。它们内部**保留了
// 原来的 sys_configs 标记检查** —— 因为已经有库被老二进制回填过了，
// 那些库这次会第一次记录 v1/v2，但步骤本身必须认出「已经做过」并变成空操作。
// 新加的步骤不要再发明这种标记：`schema_migrations` 就是记录本身。

import (
	"errors"
	"log"

	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

var steps = []Step{
	{
		Version: 1,
		Name:    "cron_prod_confirmed_backfill",
		Note:    "下发闸门上线：存量定时任务按「已确认生产变更」处理，否则它们会在凌晨被闸门静默拦掉",
		Run:     backfillCronProdConfirmed,
	},
	{
		Version: 2,
		Name:    "event_responded_at_backfill",
		Note:    "事件 SLA 上线：从事件时间线回填存量事件的响应时间，否则会凭空造出一批「响应已超时」",
		Run:     backfillEventResponded,
	},
}

// cfgCronBackfill 老二进制用来记「已回填」的键，保留只为了认出被老版本处理过的库
const cfgCronBackfill = "migration.cron_prod_confirmed_backfilled"

// backfillCronProdConfirmed 升级兼容：下发闸门上线前建的定时任务没有「生产确认」这个概念。
//
// 如果直接按未确认处理，存量任务会在下一次触发时被闸门拦掉 —— 而定时任务通常在
// 凌晨触发，没人看着，等于悄悄停掉了一批运维作业。所以这里把存量任务一次性视为
// 已确认；之后新建或编辑任务都要重新显式确认。
func backfillCronProdConfirmed(g *gorm.DB) error {
	if done, err := legacyMarkerDone(g, cfgCronBackfill); err != nil || done {
		return err
	}
	res := g.Model(&model.CronJob{}).Where("prod_confirmed = ?", false).Update("prod_confirmed", true)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[migrate] 下发闸门上线：%d 个存量定时任务按「已确认生产变更」处理，之后编辑需重新确认",
			res.RowsAffected)
	}
	return nil
}

// cfgEventRespondedBackfill 同上，老二进制的标记键
const cfgEventRespondedBackfill = "migration.event_responded_backfilled"

// backfillEventResponded 升级兼容：SLA 上线前建的事件没有 responded_at。
//
// 如果留空不管，这些单子在新界面上会一律显示「响应已超时 N 天」——
// 明明当时有人处理过，只是平台那时候没记这个时间点，等于凭空造出一批违约。
// 所以从只追加的时间线里把真实时间捞回来：最早一条 status / note 记录就是
// 「第一次有人动手」；连时间线都没有的已解决事件，退一步用解决时间
// （解决本身也是一次响应，只是把响应时间算晚了，宁可算晚也不凭空算超时）。
func backfillEventResponded(g *gorm.DB) error {
	if done, err := legacyMarkerDone(g, cfgEventRespondedBackfill); err != nil || done {
		return err
	}

	var events []model.Event
	if err := g.Where("responded_at IS NULL").Find(&events).Error; err != nil {
		return err
	}
	fromLog, fromResolved := 0, 0
	for _, event := range events {
		var first model.EventLog
		err := g.Where("event_id = ? AND action IN ?", event.ID, []string{"status", "note"}).
			Order("id asc").First(&first).Error
		switch {
		case err == nil:
			at := first.CreatedAt
			if err := g.Model(&model.Event{}).Where("id = ?", event.ID).
				Update("responded_at", &at).Error; err != nil {
				return err
			}
			fromLog++
		case errors.Is(err, gorm.ErrRecordNotFound):
			if event.ResolvedAt == nil {
				continue // 确实没人动过，留空是实话
			}
			if err := g.Model(&model.Event{}).Where("id = ?", event.ID).
				Update("responded_at", event.ResolvedAt).Error; err != nil {
				return err
			}
			fromResolved++
		default:
			return err
		}
	}
	if fromLog > 0 || fromResolved > 0 {
		log.Printf("[migrate] 事件 SLA 上线：回填响应时间 %d 条（取自时间线）+ %d 条（退回解决时间）",
			fromLog, fromResolved)
	}
	return nil
}

// legacyMarkerDone 这一步是不是已经被老二进制做过了。
//
// 只读不写：标记的角色已经由 schema_migrations 接过去，再写一份等于两个真相来源。
func legacyMarkerDone(g *gorm.DB, key string) (bool, error) {
	if !g.Migrator().HasTable(&model.SysConfig{}) {
		return false, nil
	}
	var exist model.SysConfig
	err := g.Where("`key` = ?", key).First(&exist).Error
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	default:
		return false, err
	}
}
