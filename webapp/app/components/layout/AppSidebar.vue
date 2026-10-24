<script setup lang="ts">
import type { NavLink } from '~/types/nav'
import { navMenu } from '~/constants/menus'

// 側欄：品牌區 + 導覽選單（constants/menus.ts）
const { sidebar } = useAppSettings()
</script>

<template>
  <Sidebar :collapsible="sidebar?.collapsible" :side="sidebar?.side" :variant="sidebar?.variant">
    <SidebarHeader>
      <SidebarMenu>
        <SidebarMenuItem>
          <div class="flex items-center gap-2 px-2 py-1.5">
            <div class="aspect-square size-8 flex items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
              <Icon name="i-lucide-activity" class="size-4" />
            </div>
            <div class="grid flex-1 text-left text-sm leading-tight">
              <span class="truncate font-semibold">Aliyun Guard</span>
              <span class="truncate text-xs text-muted-foreground">ECS 控制面板</span>
            </div>
          </div>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarHeader>
    <SidebarContent>
      <SidebarGroup v-for="(nav, indexGroup) in navMenu" :key="indexGroup">
        <SidebarGroupLabel v-if="nav.heading">
          {{ nav.heading }}
        </SidebarGroupLabel>
        <LayoutSidebarNavLink
          v-for="(item, index) in nav.items"
          :key="index"
          :item="item as NavLink"
        />
      </SidebarGroup>
    </SidebarContent>
    <SidebarFooter>
      <LayoutSidebarNavFooter />
    </SidebarFooter>
    <SidebarRail />
  </Sidebar>
</template>

<style scoped>

</style>
