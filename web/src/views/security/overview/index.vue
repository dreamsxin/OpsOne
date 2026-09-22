<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { securityOverview, type SecOverview } from '@/api'

const loading = ref(false)
const data = ref<SecOverview | null>(null)

const maxTrend = computed(() => {
  const list = data.value?.trend ?? []
  return list.reduce((max, x) => Math.max(max, x.total), 1)
})

const twoFactorRate = computed(() => {
  const tf = data.value?.twoFactor
  if (!tf || !tf.users) return 0
  return Math.round((tf.enabled / tf.users) * 100)
})

async function load() {
  loading.value = true
  try {
    data.value = await securityOverview()
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div v-loading="loading" class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>{{ data?.note }}</template>
      </el-alert>
      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button @click="load">刷新</el-button>
      </div>
    </el-card>

    <template v-if="data">
      <!-- 待办：值班第一眼该看的 -->
      <el-card style="margin-top: 12px">
        <template #header>待研判与待处置</template>
        <div class="stat-grid">
          <router-link to="/security/events?status=open" class="stat">
            <div class="num">{{ data.events.open }}</div>
            <div class="lbl">未结案安全事件</div>
          </router-link>
          <router-link to="/security/events?severity=critical" class="stat danger">
            <div class="num">{{ data.events.critical }}</div>
            <div class="lbl">其中严重</div>
          </router-link>
          <router-link to="/security/events?rehit=1" class="stat danger">
            <div class="num">{{ data.events.rehit }}</div>
            <div class="lbl">结案后又命中</div>
          </router-link>
          <router-link to="/security/suggestions" class="stat warn">
            <div class="num">{{ data.suggestions }}</div>
            <div class="lbl">待处理的学习建议</div>
          </router-link>
          <div class="stat">
            <div class="num">{{ data.events.escalated }}</div>
            <div class="lbl">已升格为工单</div>
          </div>
          <div class="stat">
            <div class="num">{{ data.events.new24h }}</div>
            <div class="lbl">近 24h 新增</div>
          </div>
        </div>
      </el-card>

      <!-- 拦截流水：原始事实，不去重 -->
      <el-card style="margin-top: 12px">
        <template #header>
          近 24 小时拦截流水
          <el-text type="info" size="small" style="margin-left: 8px">
            这是原始事实、不去重；研判台上的事件是它们按指纹聚合之后的结果
          </el-text>
        </template>
        <div class="stat-grid">
          <div class="stat danger">
            <div class="num">{{ data.intercept.execBlocked24h }}</div>
            <div class="lbl">下发被拦</div>
          </div>
          <div class="stat warn">
            <div class="num">{{ data.intercept.execWarn24h }}</div>
            <div class="lbl">下发告警（放过了）</div>
          </div>
          <div class="stat danger">
            <div class="num">{{ data.intercept.terminal24h }}</div>
            <div class="lbl">终端命令被拦</div>
          </div>
          <div class="stat warn">
            <div class="num">{{ data.intercept.authz24h }}</div>
            <div class="lbl">越权被拒（写操作）</div>
          </div>
          <div class="stat">
            <div class="num">{{ data.events.mutedHits }}</div>
            <div class="lbl">白名单累计挡掉</div>
          </div>
        </div>
      </el-card>

      <!-- 7 天趋势：纯 CSS 柱状，不引图表库 -->
      <el-card v-if="data.trend.length" style="margin-top: 12px">
        <template #header>近 7 天新增安全事件</template>
        <div class="trend">
          <div v-for="item in data.trend" :key="item.day" class="bar-wrap">
            <div class="bar" :style="{ height: Math.max(4, (item.total / maxTrend) * 100) + '%' }">
              <span class="bar-num">{{ item.total }}</span>
            </div>
            <div class="bar-day">{{ item.day.slice(5) }}</div>
          </div>
        </div>
      </el-card>

      <!-- 各模块现状 -->
      <div class="two-col" style="margin-top: 12px">
        <el-card>
          <template #header>暴露面</template>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="启用的扫描目标">{{ data.exposure.targets }}</el-descriptions-item>
            <el-descriptions-item label="有未登记开放端口">
              <el-tag v-if="data.exposure.unexpected" size="small" type="danger">
                {{ data.exposure.unexpected }}
              </el-tag>
              <span v-else>0</span>
            </el-descriptions-item>
            <el-descriptions-item label="扫描失败">{{ data.exposure.failed }}</el-descriptions-item>
            <el-descriptions-item label="从未扫描过">{{ data.exposure.neverScanned }}</el-descriptions-item>
            <el-descriptions-item label="基线留空">
              <el-tag v-if="data.exposure.noBaseline" size="small" type="warning">
                {{ data.exposure.noBaseline }}
              </el-tag>
              <span v-else>0</span>
              <el-text type="info" size="small" style="margin-left: 6px">
                基线留空 = 任何开放端口都会被报，会持续刷噪音
              </el-text>
            </el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card>
          <template #header>证书与域名</template>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="证书将到期 / 已过期">
              {{ data.certs.expiring }} / {{ data.certs.expired }}
            </el-descriptions-item>
            <el-descriptions-item label="证书巡检失败 / 不受信">
              {{ data.certs.error }} / {{ data.certs.untrusted }}
            </el-descriptions-item>
            <el-descriptions-item label="域名将到期 / 已过期">
              {{ data.domains.expiring }} / {{ data.domains.expired }}
            </el-descriptions-item>
            <el-descriptions-item label="域名解析漂移">
              <el-tag v-if="data.domains.dnsDrift" size="small" type="danger">
                {{ data.domains.dnsDrift }}
              </el-tag>
              <span v-else>0</span>
            </el-descriptions-item>
            <el-descriptions-item label="域名待补到期日">
              <el-tag v-if="data.domains.noExpiry" size="small" type="info">
                {{ data.domains.noExpiry }}
              </el-tag>
              <span v-else>0</span>
              <el-text type="info" size="small" style="margin-left: 6px">
                没填到期日的域名，平台对它的续费一无所知
              </el-text>
            </el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card>
          <template #header>特征库与防火墙</template>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="启用特征（其中已落到规则）">
              {{ data.signatures.total }}（{{ data.signatures.applied }}）
            </el-descriptions-item>
            <el-descriptions-item label="观察态 / 拦截态">
              <el-tag v-if="data.signatures.observe" size="small" type="warning">
                {{ data.signatures.observe }}
              </el-tag>
              <span v-else>0</span>
              / {{ data.signatures.enforce }}
              <el-text type="info" size="small" style="margin-left: 6px">
                观察态只记不拦
              </el-text>
            </el-descriptions-item>
            <el-descriptions-item label="防火墙规则（待下发）">
              {{ data.firewall.rules }}（<el-tag
                v-if="data.firewall.pending"
                size="small"
                type="warning"
                >{{ data.firewall.pending }}</el-tag
              ><span v-else>0</span>）
            </el-descriptions-item>
            <el-descriptions-item label="已下发生效">{{ data.firewall.applied }}</el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card>
          <template #header>账号与日志监控</template>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="双因子启用率">
              {{ data.twoFactor.enabled }} / {{ data.twoFactor.users }}
              <el-progress :percentage="twoFactorRate" :stroke-width="10" style="margin-top: 4px" />
            </el-descriptions-item>
            <el-descriptions-item label="日志监控点">{{ data.hostLogs.targets }}</el-descriptions-item>
            <el-descriptions-item label="命中关键字">
              <el-tag v-if="data.hostLogs.hit" size="small" type="danger">{{ data.hostLogs.hit }}</el-tag>
              <span v-else>0</span>
            </el-descriptions-item>
            <el-descriptions-item label="读不到（监控已瞎）">
              <el-tag v-if="data.hostLogs.unreadable" size="small" type="warning">
                {{ data.hostLogs.unreadable }}
              </el-tag>
              <span v-else>0</span>
            </el-descriptions-item>
          </el-descriptions>
        </el-card>
      </div>

      <!-- 这一段是这一页最该被认真读的部分 -->
      <el-card style="margin-top: 12px">
        <template #header>
          做不出真数据的部分
          <el-text type="info" size="small" style="margin-left: 8px">
            这些项**没有采集**，所以这一页里一个数字都不给 —— 用 0 冒充「没有问题」是最容易骗人的看板
          </el-text>
        </template>
        <el-table :data="data.gaps" border stripe size="small">
          <el-table-column prop="item" label="项目" width="220" />
          <el-table-column prop="why" label="为什么没有" min-width="420" />
        </el-table>
      </el-card>
    </template>
  </div>
</template>

<style scoped>
.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 12px;
}
.stat {
  padding: 14px 16px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  text-decoration: none;
  color: inherit;
  display: block;
}
.stat .num {
  font-size: 26px;
  font-weight: 600;
  line-height: 1.2;
}
.stat .lbl {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.stat.danger .num {
  color: var(--el-color-danger);
}
.stat.warn .num {
  color: var(--el-color-warning);
}
a.stat:hover {
  border-color: var(--el-color-primary);
}
.two-col {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
  gap: 12px;
}
.trend {
  display: flex;
  align-items: flex-end;
  gap: 16px;
  height: 160px;
  padding: 8px 4px 0;
}
.bar-wrap {
  flex: 1;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
}
.bar {
  width: 100%;
  max-width: 48px;
  background: var(--el-color-primary-light-3);
  border-radius: 3px 3px 0 0;
  position: relative;
  min-height: 4px;
}
.bar-num {
  position: absolute;
  top: -18px;
  left: 0;
  right: 0;
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.bar-day {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
