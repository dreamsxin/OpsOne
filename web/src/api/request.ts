import axios, { type AxiosInstance, type AxiosRequestConfig } from 'axios'
import { ElMessage } from 'element-plus'

export interface ApiBody<T = any> {
  code: number
  msg: string
  data: T
}

export interface PageData<T> {
  list: T[]
  total: number
  page: number
  pageSize: number
}

export const TOKEN_KEY = 'ops-token'

const http: AxiosInstance = axios.create({
  baseURL: '/api/v1',
  timeout: 120000
})

http.interceptors.request.use((config) => {
  const token = localStorage.getItem(TOKEN_KEY)
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (res) => res,
  (error) => {
    const status = error.response?.status
    const msg = error.response?.data?.msg || error.message || '请求失败'

    if (status === 401) {
      localStorage.removeItem(TOKEN_KEY)
      // 避免在登录页重复跳转
      if (!location.hash.includes('/login')) {
        location.href = '/login'
      }
    } else {
      ElMessage.error(msg)
    }
    return Promise.reject(new Error(msg))
  }
)

/** 统一解包 {code,msg,data} 结构，业务代码只关心 data */
export async function request<T = any>(config: AxiosRequestConfig): Promise<T> {
  const res = await http.request<ApiBody<T>>(config)
  if (res.data.code !== 0) {
    ElMessage.error(res.data.msg)
    throw new Error(res.data.msg)
  }
  return res.data.data
}

export default http
